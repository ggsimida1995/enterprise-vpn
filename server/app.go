package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxJSONBody = 1 << 20

// Store is the only authoritative source for users, devices, networks and
// permissions. It is intentionally small for the first deployment phase.
type Store struct {
	mu       sync.RWMutex
	path     string
	data     State
	fileHash string
}

type State struct {
	Admin    AdminAccount       `json:"admin"`
	Users    []User             `json:"users"`
	Networks map[string]Network `json:"networks"`
	Devices  map[string]Device  `json:"devices"`
	Sessions map[string]Session `json:"sessions,omitempty"`
}

// AdminAccount is the server-side Web administration account. It is separate
// from enterprise VPN client users and is never included in client responses.
type AdminAccount struct {
	Username           string `json:"username"`
	PasswordHash       string `json:"password_hash"`
	PasswordSalt       string `json:"password_salt"`
	MustChangePassword bool   `json:"must_change_password"`
}

type User struct {
	ID             string              `json:"id"`
	Username       string              `json:"username"`
	PasswordHash   string              `json:"password_hash"`
	PasswordSalt   string              `json:"password_salt"`
	NetworkIDs     []string            `json:"network_ids"`
	AllowedSubnets map[string][]string `json:"allowed_subnets,omitempty"`
	Revision       uint64              `json:"revision"`
}

type Network struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Secret           string         `json:"secret"`
	Gateway          string         `json:"gateway,omitempty"`
	GatewayDeviceIDs []string       `json:"gateway_device_ids,omitempty"`
	VirtualCIDR      string         `json:"virtual_cidr"`
	Subnets          []string       `json:"subnets"`
	ProxyNetworks    []ProxyNetwork `json:"proxy_networks,omitempty"`
	RelayNodes       []string       `json:"relay_nodes,omitempty"`
	PeerNodes        []string       `json:"peer_nodes,omitempty"`
}

// ProxyNetwork describes a private subnet published by a designated Gateway
// through EasyTier's subnet-proxy feature. Ordinary client devices never get
// this block in their generated config.
type ProxyNetwork struct {
	CIDR       string   `json:"cidr"`
	MappedCIDR string   `json:"mapped_cidr,omitempty"`
	Allow      []string `json:"allow,omitempty"`
}

type Device struct {
	ID            string            `json:"id"`
	UserID        string            `json:"user_id"`
	Platform      string            `json:"platform"`
	ClientVersion string            `json:"client_version"`
	VirtualIP     string            `json:"virtual_ip,omitempty"`
	VirtualIPs    map[string]string `json:"virtual_ips,omitempty"`
	Status        string            `json:"status"`
	LastSeen      time.Time         `json:"last_seen"`
}

type Session struct {
	Token    string    `json:"token"`
	UserID   string    `json:"user_id"`
	DeviceID string    `json:"device_id"`
	Expires  time.Time `json:"expires"`
}

type ClientRequest struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	DeviceID      string `json:"device_id"`
	Platform      string `json:"platform"`
	ClientVersion string `json:"client_version"`
}

type HeartbeatRequest struct {
	DeviceID string `json:"device_id"`
}

type RuntimeConfig struct {
	Revision           string            `json:"revision"`
	NetworkName        string            `json:"network_name"`
	AccessibleNetworks []string          `json:"accessible_networks"`
	VirtualIP          string            `json:"virtual_ip"`
	VirtualIPs         map[string]string `json:"virtual_ips,omitempty"`
	EasyTierConfig     string            `json:"easytier_config"`
	EasyTierConfigs    []string          `json:"easytier_configs,omitempty"`
}

type ClientResponse struct {
	Token  string        `json:"token,omitempty"`
	User   UserView      `json:"user"`
	Config RuntimeConfig `json:"config"`
}

type UserView struct {
	Username string `json:"username"`
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		s.data = defaultState()
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, fmt.Errorf("decode server state: %w", err)
	}
	if s.data.Networks == nil {
		s.data.Networks = map[string]Network{}
	}
	if s.data.Devices == nil {
		s.data.Devices = map[string]Device{}
	}
	if s.data.Sessions == nil {
		s.data.Sessions = map[string]Session{}
	}
	adminAdded := false
	if s.data.Admin == (AdminAccount{}) {
		s.data.Admin = defaultAdminAccount()
		adminAdded = true
	}
	s.normalizeDevicesLocked()
	if err := validateState(s.data); err != nil {
		return nil, err
	}
	s.fileHash = contentDigest(b)
	if adminAdded {
		if err := s.persistLocked(); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func defaultState() State {
	salt := "enterprise-vpn-demo-salt"
	return State{
		Admin: defaultAdminAccount(),
		Users: []User{{ID: "user-demo", Username: "demo", PasswordHash: hashPassword("demo", salt), PasswordSalt: salt, NetworkIDs: []string{"company"}, Revision: 1}},
		Networks: map[string]Network{"company": {
			ID: "company", Name: "公司内网", Secret: "replace-this-network-secret", Gateway: "192.168.10.1",
			VirtualCIDR: "10.144.0.0/16", Subnets: []string{"192.168.10.0/24", "192.168.20.0/24"},
		}},
		Devices:  map[string]Device{},
		Sessions: map[string]Session{},
	}
}

func defaultAdminAccount() AdminAccount {
	const password = "admin"
	const salt = "enterprise-vpn-initial-admin-salt"
	return AdminAccount{
		Username:           "admin",
		PasswordHash:       hashPassword(password, salt),
		PasswordSalt:       salt,
		MustChangePassword: true,
	}
}

func validateState(state State) error {
	if strings.TrimSpace(state.Admin.Username) == "" || state.Admin.PasswordHash == "" || state.Admin.PasswordSalt == "" {
		return errors.New("server state has no admin password verifier")
	}
	seenUsers := make(map[string]bool, len(state.Users))
	seenUserIDs := make(map[string]bool, len(state.Users))
	for _, user := range state.Users {
		if user.ID == "" || user.Username == "" || seenUsers[user.Username] || seenUserIDs[user.ID] {
			return errors.New("server state contains an invalid or duplicate user")
		}
		seenUsers[user.Username] = true
		seenUserIDs[user.ID] = true
		if user.PasswordHash == "" || user.PasswordSalt == "" {
			return fmt.Errorf("user %q has no password verifier", user.Username)
		}
		seenNetworks := make(map[string]bool, len(user.NetworkIDs))
		for _, networkID := range user.NetworkIDs {
			if _, ok := state.Networks[networkID]; !ok {
				return fmt.Errorf("user %q references unknown network %q", user.Username, networkID)
			}
			if seenNetworks[networkID] {
				return fmt.Errorf("user %q has duplicate network %q", user.Username, networkID)
			}
			seenNetworks[networkID] = true
		}
		for networkID, subnets := range user.AllowedSubnets {
			network, ok := state.Networks[networkID]
			if !ok {
				return fmt.Errorf("user %q scopes unknown network %q", user.Username, networkID)
			}
			if !containsString(user.NetworkIDs, networkID) {
				return fmt.Errorf("user %q scopes network %q without authorizing it", user.Username, networkID)
			}
			allowed := make(map[string]bool, len(effectiveProxyNetworks(network)))
			for _, proxy := range effectiveProxyNetworks(network) {
				allowed[proxy.CIDR] = true
			}
			seen := map[string]bool{}
			for _, subnet := range subnets {
				if !isIPv4CIDR(subnet) {
					return fmt.Errorf("user %q has invalid allowed subnet %q", user.Username, subnet)
				}
				if !allowed[subnet] {
					return fmt.Errorf("user %q is not allowed to scope subnet %q in network %q", user.Username, subnet, networkID)
				}
				if seen[subnet] {
					return fmt.Errorf("user %q has duplicate allowed subnet %q", user.Username, subnet)
				}
				seen[subnet] = true
			}
		}
	}
	for id, network := range state.Networks {
		if network.ID == "" || network.ID != id || network.Name == "" {
			return fmt.Errorf("network %q has invalid identity", id)
		}
		if !isIPv4CIDR(network.VirtualCIDR) {
			return fmt.Errorf("network %q has invalid virtual_cidr", id)
		}
		if network.Gateway != "" {
			if parsed := net.ParseIP(network.Gateway); parsed == nil || parsed.To4() == nil {
				return fmt.Errorf("network %q has invalid gateway %q", id, network.Gateway)
			}
		}
		for _, subnet := range network.Subnets {
			if !isIPv4CIDR(subnet) {
				return fmt.Errorf("network %q has invalid subnet %q", id, subnet)
			}
		}
		for _, deviceID := range network.GatewayDeviceIDs {
			if !validDeviceID(deviceID) {
				return fmt.Errorf("network %q has invalid gateway device id %q", id, deviceID)
			}
		}
		for _, peer := range append(append([]string(nil), network.PeerNodes...), network.RelayNodes...) {
			if !validPeerURI(peer) {
				return fmt.Errorf("network %q has invalid peer uri %q", id, peer)
			}
		}
		seenProxy := map[string]bool{}
		for _, proxy := range effectiveProxyNetworks(network) {
			if !isIPv4CIDR(proxy.CIDR) {
				return fmt.Errorf("network %q has invalid proxy cidr %q", id, proxy.CIDR)
			}
			if proxy.MappedCIDR != "" {
				if !isIPv4CIDR(proxy.MappedCIDR) {
					return fmt.Errorf("network %q has invalid proxy mapped_cidr %q", id, proxy.MappedCIDR)
				}
				proxyPrefix, _ := ipv4CIDRPrefix(proxy.CIDR)
				mappedPrefix, _ := ipv4CIDRPrefix(proxy.MappedCIDR)
				if proxyPrefix != mappedPrefix {
					return fmt.Errorf("network %q proxy cidr and mapped_cidr must use the same prefix length", id)
				}
			}
			if seenProxy[proxy.CIDR] {
				return fmt.Errorf("network %q contains duplicate proxy network %q", id, proxy.CIDR)
			}
			seenProxy[proxy.CIDR] = true
			for _, protocol := range proxy.Allow {
				switch strings.ToLower(protocol) {
				case "tcp", "udp", "icmp":
				default:
					return fmt.Errorf("network %q has invalid proxy protocol %q", id, protocol)
				}
			}
		}
	}
	usersByID := make(map[string]bool, len(state.Users))
	for _, user := range state.Users {
		usersByID[user.ID] = true
	}
	for id, device := range state.Devices {
		if !validDeviceID(id) || device.ID != id || !usersByID[device.UserID] {
			return fmt.Errorf("device %q has invalid identity or user binding", id)
		}
		for networkID, virtualIP := range device.VirtualIPs {
			network, ok := state.Networks[networkID]
			if !ok {
				continue
			}
			if !ipv4InCIDR(virtualIP, network.VirtualCIDR) {
				return fmt.Errorf("device %q has virtual ip %q outside network %q", id, virtualIP, networkID)
			}
		}
	}
	for token, session := range state.Sessions {
		if token == "" || session.Token != token || !usersByID[session.UserID] {
			return fmt.Errorf("session %q has invalid user binding", token)
		}
		device, ok := state.Devices[session.DeviceID]
		if !ok || device.UserID != session.UserID {
			return fmt.Errorf("session %q has invalid device binding", token)
		}
	}
	return nil
}

func (s *Store) persistLocked() error {
	if dir := filepath.Dir(s.path); dir != "." {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".enterprise-vpn-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	var encoded bytes.Buffer
	enc := json.NewEncoder(&encoded)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s.data); err != nil {
		return err
	}
	if _, err := tmp.Write(encoded.Bytes()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		return err
	}
	s.fileHash = contentDigest(encoded.Bytes())
	return nil
}

// SetPassword changes a local account verifier without ever persisting the
// clear-text password.
func (s *Store) SetPassword(username, password string) error {
	if strings.TrimSpace(username) == "" || password == "" {
		return errors.New("username and password are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Users {
		if s.data.Users[i].Username != username {
			continue
		}
		salt := randomToken()
		if len(salt) > 32 {
			salt = salt[:32]
		}
		s.data.Users[i].PasswordSalt = salt
		s.data.Users[i].PasswordHash = hashPassword(password, salt)
		s.data.Users[i].Revision++
		return s.persistLocked()
	}
	return fmt.Errorf("user %q not found", username)
}

func (s *Store) normalizeDevicesLocked() {
	users := make(map[string]User, len(s.data.Users))
	for _, user := range s.data.Users {
		users[user.ID] = user
	}
	for id, device := range s.data.Devices {
		if device.VirtualIPs == nil {
			device.VirtualIPs = map[string]string{}
		}
		if device.VirtualIP != "" && len(device.VirtualIPs) == 0 {
			if user, ok := users[device.UserID]; ok && len(user.NetworkIDs) > 0 {
				device.VirtualIPs[user.NetworkIDs[0]] = device.VirtualIP
			}
		}
		if user, ok := users[device.UserID]; ok {
			device = ensureDeviceVirtualIPs(s.data, user, device)
		}
		s.data.Devices[id] = device
	}
}

// reloadIfChangedLocked lets an operator update the JSON state file while the
// server is running. The next client heartbeat observes the new permissions.
func (s *Store) reloadIfChangedLocked() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if contentDigest(b) == s.fileHash {
		return nil
	}
	var data State
	if err := json.Unmarshal(b, &data); err != nil {
		return fmt.Errorf("decode updated server state: %w", err)
	}
	previous := s.data
	// Runtime sessions and device leases belong to the running server, not to
	// the operator's network/permission edit. Keep them when an admin writes a
	// control-only JSON document without those fields.
	if data.Sessions == nil {
		data.Sessions = s.data.Sessions
	}
	if data.Admin == (AdminAccount{}) {
		data.Admin = s.data.Admin
	}
	if data.Devices == nil {
		data.Devices = s.data.Devices
	}
	if data.Networks == nil {
		data.Networks = map[string]Network{}
	}
	if data.Devices == nil {
		data.Devices = map[string]Device{}
	}
	if data.Sessions == nil {
		data.Sessions = map[string]Session{}
	}
	s.data = data
	s.normalizeDevicesLocked()
	if err := validateState(s.data); err != nil {
		s.data = previous
		return err
	}
	s.fileHash = contentDigest(b)
	return nil
}

func NewHandler(store *Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("POST /api/client/login", store.handleClientAuth)
	mux.HandleFunc("POST /api/client/heartbeat", store.handleHeartbeat)
	mux.HandleFunc("POST /api/client/logout", store.handleLogout)
	admin := adminRoutes(store)
	mux.Handle("/admin", admin)
	mux.Handle("/api/admin/", admin)
	return withJSONLimit(mux)
}

func withJSONLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(r.Context()))
	})
}

func (s *Store) handleClientAuth(w http.ResponseWriter, r *http.Request) {
	var req ClientRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Username == "" || req.Password == "" || !validDeviceID(req.DeviceID) {
		writeError(w, http.StatusBadRequest, "username, password and device_id are required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadIfChangedLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, "reload server state")
		return
	}
	before, err := cloneState(s.data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "snapshot server state")
		return
	}
	var user *User
	for i := range s.data.Users {
		if s.data.Users[i].Username == req.Username {
			user = &s.data.Users[i]
			break
		}
	}
	if user == nil || !checkPassword(req.Password, user.PasswordHash, user.PasswordSalt) {
		writeError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	device := s.data.Devices[req.DeviceID]
	if device.ID != "" && device.UserID != user.ID {
		writeError(w, http.StatusConflict, "device is already bound to another user")
		return
	}
	if device.ID == "" {
		device.ID = req.DeviceID
		device.UserID = user.ID
	}
	device = ensureDeviceVirtualIPs(s.data, *user, device)
	config, err := buildRuntimeConfig(*user, device, s.data.Networks)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	device.Platform, device.ClientVersion, device.Status, device.LastSeen = req.Platform, req.ClientVersion, "online", time.Now().UTC()
	s.data.Devices[req.DeviceID] = device
	for token, session := range s.data.Sessions {
		if session.DeviceID == req.DeviceID {
			delete(s.data.Sessions, token)
		}
	}
	token := randomToken()
	s.data.Sessions[token] = Session{Token: token, UserID: user.ID, DeviceID: req.DeviceID, Expires: time.Now().UTC().Add(24 * time.Hour)}
	if err := s.persistLocked(); err != nil {
		s.data = before
		writeError(w, http.StatusInternalServerError, "persist server state")
		return
	}
	writeJSON(w, http.StatusOK, ClientResponse{Token: token, User: UserView{Username: user.Username}, Config: config})
}

func (s *Store) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	var req HeartbeatRequest
	if err := decodeJSON(r, &req); err != nil || !validDeviceID(req.DeviceID) {
		writeError(w, http.StatusBadRequest, "device_id is required")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadIfChangedLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, "reload server state")
		return
	}
	before, err := cloneState(s.data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "snapshot server state")
		return
	}
	session, ok := s.data.Sessions[token]
	if !ok || session.Expires.Before(time.Now()) || session.DeviceID != req.DeviceID {
		writeError(w, http.StatusUnauthorized, "session expired")
		return
	}
	var user User
	for _, candidate := range s.data.Users {
		if candidate.ID == session.UserID {
			user = candidate
			break
		}
	}
	device, ok := s.data.Devices[req.DeviceID]
	if !ok || user.ID == "" {
		writeError(w, http.StatusUnauthorized, "device or user not found")
		return
	}
	device = ensureDeviceVirtualIPs(s.data, user, device)
	config, err := buildRuntimeConfig(user, device, s.data.Networks)
	if err != nil {
		// Never leave a previously authorized Core running when the operator
		// removes or invalidates the user's network assignment.
		writeError(w, http.StatusForbidden, err.Error())
		return
	}
	device.Status, device.LastSeen = "online", time.Now().UTC()
	s.data.Devices[req.DeviceID] = device
	if err := s.persistLocked(); err != nil {
		s.data = before
		writeError(w, http.StatusInternalServerError, "persist server state")
		return
	}
	writeJSON(w, http.StatusOK, ClientResponse{User: UserView{Username: user.Username}, Config: config})
}

func (s *Store) handleLogout(w http.ResponseWriter, r *http.Request) {
	token := bearerToken(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadIfChangedLocked(); err != nil {
		writeError(w, http.StatusInternalServerError, "reload server state")
		return
	}
	before, err := cloneState(s.data)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "snapshot server state")
		return
	}
	session, ok := s.data.Sessions[token]
	if !ok {
		writeError(w, http.StatusUnauthorized, "session expired")
		return
	}
	delete(s.data.Sessions, token)
	if device, exists := s.data.Devices[session.DeviceID]; exists {
		device.Status = "offline"
		device.LastSeen = time.Now().UTC()
		s.data.Devices[session.DeviceID] = device
	}
	if err := s.persistLocked(); err != nil {
		s.data = before
		writeError(w, http.StatusInternalServerError, "persist server state")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func buildRuntimeConfig(user User, device Device, networks map[string]Network) (RuntimeConfig, error) {
	if len(user.NetworkIDs) == 0 {
		return RuntimeConfig{}, errors.New("user has no authorized network")
	}
	var selected Network
	var configs []string
	var routes []string
	virtualIPs := make(map[string]string, len(user.NetworkIDs))
	seen := map[string]bool{}
	for _, id := range user.NetworkIDs {
		network, ok := networks[id]
		if !ok {
			return RuntimeConfig{}, fmt.Errorf("authorized network %q does not exist", id)
		}
		if selected.ID == "" {
			selected = network
		}
		virtualIP := device.VirtualIPs[id]
		if virtualIP == "" && id == user.NetworkIDs[0] {
			virtualIP = device.VirtualIP
		}
		virtualIPs[id] = virtualIP
		// Subnet routes are published by the designated Gateway through
		// [[proxy_network]]. A normal client receives no local route entries.
		allowedProxies, err := authorizedProxyNetworks(user, network)
		if err != nil {
			return RuntimeConfig{}, err
		}
		allowedCIDRs := make([]string, 0, len(allowedProxies))
		for _, proxy := range allowedProxies {
			accessCIDR := proxyAccessCIDR(proxy)
			if !containsString(allowedCIDRs, accessCIDR) {
				allowedCIDRs = append(allowedCIDRs, accessCIDR)
			}
		}
		gateway := isGatewayDevice(network, device.ID)
		if gateway {
			allowedProxies = effectiveProxyNetworks(network)
			allowedCIDRs = allowedCIDRs[:0]
			for _, proxy := range allowedProxies {
				accessCIDR := proxyAccessCIDR(proxy)
				if !containsString(allowedCIDRs, accessCIDR) {
					allowedCIDRs = append(allowedCIDRs, accessCIDR)
				}
			}
		}
		aclCIDRs := allowedCIDRs
		if gateway {
			// Gateway ACLs would apply to the infrastructure node itself. Its
			// only job is to publish the server-authorized proxy networks.
			aclCIDRs = nil
		}
		configs = append(configs, renderEasyTierConfigForDevice(user, device, network, nil, aclCIDRs, virtualIP, gateway))
		for _, proxy := range allowedProxies {
			if !isIPv4CIDR(proxy.CIDR) {
				return RuntimeConfig{}, fmt.Errorf("invalid proxy cidr %q", proxy.CIDR)
			}
			accessCIDR := proxyAccessCIDR(proxy)
			if !seen[accessCIDR] {
				seen[accessCIDR] = true
				routes = append(routes, accessCIDR)
			}
		}
	}
	revision := configRevision(user, device, virtualIPs, networks)
	return RuntimeConfig{
		Revision: revision, NetworkName: selected.Name, AccessibleNetworks: routes, VirtualIP: virtualIPs[selected.ID], VirtualIPs: virtualIPs,
		EasyTierConfig: configs[0], EasyTierConfigs: configs,
	}, nil
}

func renderEasyTierConfig(user User, device Device, network Network, routes []string, virtualIP string) string {
	return renderEasyTierConfigForDevice(user, device, network, routes, nil, virtualIP, false)
}

func renderEasyTierConfigForDevice(user User, device Device, network Network, routes, allowedSubnets []string, virtualIP string, isGateway bool) string {
	var b strings.Builder
	b.WriteString("# Managed by enterprise-vpn. Do not edit.\n")
	b.WriteString("instance_name = \"enterprise-vpn\"\n")
	b.WriteString("instance_id = " + strconv.Quote(runtimeInstanceID(device.ID, network.ID)) + "\n")
	b.WriteString("ipv4 = " + strconv.Quote(virtualIP) + "\n")
	b.WriteString("listeners = []\n")
	b.WriteString("routes = [")
	for i, route := range routes {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Quote(route))
	}
	b.WriteString("]\n\n[network_identity]\n")
	b.WriteString("network_name = " + strconv.Quote(network.Name) + "\n")
	b.WriteString("network_secret = " + strconv.Quote(network.Secret) + "\n")
	if allowedSubnets != nil {
		b.WriteString("\n[acl.acl_v1]\n")
		b.WriteString("[[acl.acl_v1.chains]]\n")
		b.WriteString("name = \"enterprise-subnet-access\"\n")
		b.WriteString("chain_type = 3\n")
		b.WriteString("enabled = true\n")
		b.WriteString("default_action = 2\n")
		if len(allowedSubnets) > 0 {
			b.WriteString("\n[[acl.acl_v1.chains.rules]]\n")
			b.WriteString("name = \"allow-authorized-subnets\"\n")
			b.WriteString("priority = 1000\n")
			b.WriteString("enabled = true\n")
			b.WriteString("action = 1\n")
			b.WriteString("protocol = 5\n")
			b.WriteString("stateful = true\n")
			b.WriteString("destination_ips = [")
			for i, subnet := range allowedSubnets {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(strconv.Quote(subnet))
			}
			b.WriteString("]\n")
		}
	}
	for _, peer := range network.PeerNodes {
		b.WriteString("\n[[peer]]\nuri = " + strconv.Quote(peer) + "\n")
	}
	for _, peer := range network.RelayNodes {
		b.WriteString("\n[[peer]]\nuri = " + strconv.Quote(peer) + "\n")
	}
	if isGateway {
		for _, proxy := range effectiveProxyNetworks(network) {
			b.WriteString("\n[[proxy_network]]\n")
			b.WriteString("cidr = " + strconv.Quote(proxy.CIDR) + "\n")
			if proxy.MappedCIDR != "" {
				b.WriteString("mapped_cidr = " + strconv.Quote(proxy.MappedCIDR) + "\n")
			}
			if len(proxy.Allow) > 0 {
				b.WriteString("allow = [")
				for i, protocol := range proxy.Allow {
					if i > 0 {
						b.WriteString(", ")
					}
					b.WriteString(strconv.Quote(strings.ToLower(protocol)))
				}
				b.WriteString("]\n")
			}
		}
	}
	_ = user // user identity is represented by the server-issued instance ID.
	return b.String()
}

func isGatewayDevice(network Network, deviceID string) bool {
	for _, gatewayDeviceID := range network.GatewayDeviceIDs {
		if gatewayDeviceID == deviceID {
			return true
		}
	}
	return false
}

func effectiveProxyNetworks(network Network) []ProxyNetwork {
	if len(network.ProxyNetworks) > 0 {
		return network.ProxyNetworks
	}
	proxies := make([]ProxyNetwork, 0, len(network.Subnets))
	for _, subnet := range network.Subnets {
		proxies = append(proxies, ProxyNetwork{CIDR: subnet})
	}
	return proxies
}

func proxyAccessCIDR(proxy ProxyNetwork) string {
	if proxy.MappedCIDR != "" {
		return proxy.MappedCIDR
	}
	return proxy.CIDR
}

func authorizedProxyNetworks(user User, network Network) ([]ProxyNetwork, error) {
	proxies := effectiveProxyNetworks(network)
	requested, scoped := user.AllowedSubnets[network.ID]
	if !scoped {
		return proxies, nil
	}
	byCIDR := make(map[string]ProxyNetwork, len(proxies))
	for _, proxy := range proxies {
		byCIDR[proxy.CIDR] = proxy
	}
	selected := make([]ProxyNetwork, 0, len(requested))
	for _, cidr := range requested {
		proxy, ok := byCIDR[cidr]
		if !ok {
			return nil, fmt.Errorf("user %q is not authorized for subnet %q in network %q", user.Username, cidr, network.ID)
		}
		selected = append(selected, proxy)
	}
	return selected, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func isIPv4CIDR(value string) bool {
	_, ok := ipv4CIDRPrefix(value)
	return ok
}

func validPeerURI(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func contentDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func ipv4CIDRPrefix(value string) (int, bool) {
	ip, network, err := net.ParseCIDR(value)
	if err != nil || ip.To4() == nil || !ip.Equal(network.IP) {
		return 0, false
	}
	ones, _ := network.Mask.Size()
	return ones, true
}

func runtimeInstanceID(deviceID, networkID string) string {
	sum := sha256.Sum256([]byte(deviceID + "\x00" + networkID))
	bytes := sum[:16]
	bytes[6] = (bytes[6] & 0x0f) | 0x50
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", bytes[0:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:16])
}

func ensureDeviceVirtualIPs(state State, user User, device Device) Device {
	if device.VirtualIPs == nil {
		device.VirtualIPs = map[string]string{}
	}
	for _, networkID := range user.NetworkIDs {
		network, ok := state.Networks[networkID]
		if device.VirtualIPs[networkID] == "" || !ok || !ipv4InCIDR(device.VirtualIPs[networkID], network.VirtualCIDR) {
			device.VirtualIPs[networkID] = allocateVirtualIP(state, device.ID, networkID)
		}
	}
	if len(user.NetworkIDs) > 0 {
		device.VirtualIP = device.VirtualIPs[user.NetworkIDs[0]]
	}
	return device
}

func ipv4InCIDR(ipValue, cidrValue string) bool {
	ip := net.ParseIP(ipValue)
	if ip == nil || ip.To4() == nil {
		return false
	}
	_, network, err := net.ParseCIDR(cidrValue)
	return err == nil && network.Contains(ip)
}

func allocateVirtualIP(state State, deviceID, networkID string) string {
	network, ok := state.Networks[networkID]
	if ok {
		if _, cidr, err := net.ParseCIDR(network.VirtualCIDR); err == nil {
			if base := cidr.IP.To4(); base != nil {
				used := map[string]bool{}
				for _, device := range state.Devices {
					if device.VirtualIPs != nil {
						used[device.VirtualIPs[networkID]] = true
					} else {
						used[device.VirtualIP] = true
					}
				}
				baseValue := uint32(base[0])<<24 | uint32(base[1])<<16 | uint32(base[2])<<8 | uint32(base[3])
				for host := uint32(10); host < 65535; host++ {
					value := baseValue + host
					ip := net.IPv4(byte(value>>24), byte(value>>16), byte(value>>8), byte(value)).String()
					if cidr.Contains(net.ParseIP(ip)) && !used[ip] {
						return ip
					}
				}
			}
		}
	}
	// A stable fallback keeps login useful even when an operator omitted CIDR.
	sum := sha256.Sum256([]byte(deviceID + "\x00" + networkID))
	return fmt.Sprintf("10.254.%d.%d", sum[0], sum[1])
}

func configRevision(user User, device Device, virtualIPs map[string]string, networks map[string]Network) string {
	type revisionInput struct {
		UserID         string              `json:"user_id"`
		NetworkIDs     []string            `json:"network_ids"`
		AllowedSubnets map[string][]string `json:"allowed_subnets,omitempty"`
		UserRevision   uint64              `json:"user_revision"`
		Networks       []Network           `json:"networks"`
		DeviceID       string              `json:"device_id"`
		VirtualIPs     map[string]string   `json:"virtual_ips"`
	}
	selected := make([]Network, 0, len(user.NetworkIDs))
	for _, id := range user.NetworkIDs {
		if network, ok := networks[id]; ok {
			selected = append(selected, network)
		}
	}
	b, _ := json.Marshal(revisionInput{user.ID, user.NetworkIDs, user.AllowedSubnets, user.Revision, selected, device.ID, virtualIPs})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func decodeJSON(r *http.Request, dst any) error {
	b, err := io.ReadAll(io.LimitReader(r.Body, maxJSONBody+1))
	if err != nil {
		return err
	}
	if len(b) > maxJSONBody {
		return errors.New("request body is too large")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if strings.HasPrefix(value, prefix) {
		return strings.TrimSpace(strings.TrimPrefix(value, prefix))
	}
	return ""
}

func validDeviceID(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	for i, r := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashPassword(password, salt string) string {
	digest := sha256.Sum256([]byte(salt + "\x00" + password))
	for i := 0; i < 120000; i++ {
		next := sha256.Sum256(append(digest[:], byte(i), byte(i>>8), byte(i>>16), byte(i>>24)))
		digest = next
	}
	return hex.EncodeToString(digest[:])
}

func checkPassword(password, expected, salt string) bool {
	actual := hashPassword(password, salt)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func cloneState(state State) (State, error) {
	b, err := json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	var clone State
	if err := json.Unmarshal(b, &clone); err != nil {
		return State{}, err
	}
	return clone, nil
}
