package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientLoginHeartbeat(t *testing.T) {
	path := t.TempDir() + "/server.json"
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(store))
	defer server.Close()

	deviceID := "11111111-1111-4111-8111-111111111111"
	body := `{"username":"demo","password":"demo","device_id":"` + deviceID + `","platform":"test","client_version":"dev"}`
	resp, err := http.Post(server.URL+"/api/client/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	var result ClientResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Token == "" || result.Config.EasyTierConfig == "" || len(result.Config.AccessibleNetworks) != 2 {
		t.Fatalf("unexpected response: %+v", result)
	}

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/client/heartbeat", strings.NewReader(`{"device_id":"`+deviceID+`"}`))
	req.Header.Set("Authorization", "Bearer "+result.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat status = %d", resp.StatusCode)
	}
}

func TestStorePersists(t *testing.T) {
	path := t.TempDir() + "/nested/server.json"
	if _, err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path); err != nil {
		t.Fatal(err)
	}
}

func TestAdminConfigRequiresAuthAndPreservesSessions(t *testing.T) {
	path := t.TempDir() + "/server.json"
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandlerWithAdmin(store, AdminAuth{Username: "operator", Password: "secret"})
	server := httptest.NewServer(handler)
	defer server.Close()

	response, err := http.Get(server.URL + "/admin")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated admin status = %d", response.StatusCode)
	}
	_ = response.Body.Close()

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/admin/config", nil)
	request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("operator:secret")))
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	var config AdminConfig
	if err := json.NewDecoder(response.Body).Decode(&config); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || len(config.Users) != 1 || config.Users[0].Password != "" {
		t.Fatalf("admin config = %+v, status = %d", config, response.StatusCode)
	}
	request, _ = http.NewRequest(http.MethodGet, server.URL+"/admin", nil)
	request.SetBasicAuth("operator", "secret")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !bytes.Contains(page, []byte("企业内网服务端配置")) {
		t.Fatalf("admin page status/body = %d/%q", response.StatusCode, page)
	}

	config.Users[0].Password = "changed"
	config.Networks["company"] = Network{
		ID: "company", Name: "公司内网", Secret: "managed-secret", Gateway: "192.168.10.1",
		VirtualCIDR: "10.144.0.0/16", Subnets: []string{"192.168.10.0/24"},
		PeerNodes: []string{"tcp://10.0.0.1:11010"},
	}
	payload, _ := json.Marshal(config)
	request, _ = http.NewRequest(http.MethodPut, server.URL+"/api/admin/config", bytes.NewReader(payload))
	request.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("operator:secret")))
	request.Header.Set("Content-Type", "application/json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("admin save status = %d", response.StatusCode)
	}
	if !checkPassword("changed", store.data.Users[0].PasswordHash, store.data.Users[0].PasswordSalt) {
		t.Fatal("admin password was not updated")
	}
	if store.data.Networks["company"].PeerNodes[0] != "tcp://10.0.0.1:11010" {
		t.Fatalf("network config was not updated: %+v", store.data.Networks["company"])
	}
}

func TestRequiredAdminAuth(t *testing.T) {
	t.Setenv("VPN_ADMIN_USER", "operator")
	t.Setenv("VPN_ADMIN_PASSWORD", "secret")
	auth, err := requiredAdminAuth()
	if err != nil || auth.Username != "operator" || auth.Password != "secret" {
		t.Fatalf("required admin auth = %+v, %v", auth, err)
	}
	t.Setenv("VPN_ADMIN_PASSWORD", "")
	if _, err := requiredAdminAuth(); err == nil {
		t.Fatal("missing password should fail")
	}
}

func TestSetPasswordUpdatesVerifier(t *testing.T) {
	path := t.TempDir() + "/server.json"
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetPassword("demo", "new-password"); err != nil {
		t.Fatal(err)
	}
	if !checkPassword("new-password", store.data.Users[0].PasswordHash, store.data.Users[0].PasswordSalt) {
		t.Fatal("new password does not verify")
	}
	if checkPassword("demo", store.data.Users[0].PasswordHash, store.data.Users[0].PasswordSalt) {
		t.Fatal("old password still verifies")
	}
	reloaded, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !checkPassword("new-password", reloaded.data.Users[0].PasswordHash, reloaded.data.Users[0].PasswordSalt) {
		t.Fatal("persisted password does not verify")
	}
}

func TestAuthorizedNetworksProduceSeparateCoreConfigs(t *testing.T) {
	state := defaultState()
	state.Networks["finance"] = Network{
		ID: "finance", Name: "财务内网", Secret: "finance-secret", VirtualCIDR: "10.145.0.0/16", Subnets: []string{"192.168.30.0/24"},
	}
	user := state.Users[0]
	user.NetworkIDs = []string{"company", "finance"}
	config, err := buildRuntimeConfig(user, Device{ID: "device-test", VirtualIPs: map[string]string{"company": "10.144.0.10", "finance": "10.145.0.10"}}, state.Networks)
	if err != nil {
		t.Fatal(err)
	}
	if len(config.EasyTierConfigs) != 2 || !strings.Contains(config.EasyTierConfigs[1], `network_name = "财务内网"`) {
		t.Fatalf("expected one core config per network: %+v", config)
	}
	if strings.Contains(config.EasyTierConfigs[0], `ipv4 = "10.145.0.10"`) || !strings.Contains(config.EasyTierConfigs[1], `ipv4 = "10.145.0.10"`) {
		t.Fatalf("network-specific virtual IPs were not preserved: %+v", config.EasyTierConfigs)
	}
	if strings.Contains(config.EasyTierConfigs[0], `instance_id = "`+user.ID+`"`) || strings.Contains(config.EasyTierConfigs[1], `instance_id = "`+user.ID+`"`) {
		t.Fatal("runtime instance ids must not expose or reuse the user id")
	}
	if strings.Contains(config.EasyTierConfigs[0], `instance_id = "`+runtimeInstanceID("device-test", "finance")+`"`) || !strings.Contains(config.EasyTierConfigs[1], `instance_id = "`+runtimeInstanceID("device-test", "finance")+`"`) {
		t.Fatalf("network-specific runtime instance ids were not preserved: %+v", config.EasyTierConfigs)
	}
}

func TestGatewayConfigIncludesSubnetProxyOnlyForGatewayDevice(t *testing.T) {
	state := defaultState()
	deviceID := "33333333-3333-4333-8333-333333333333"
	network := state.Networks["company"]
	network.GatewayDeviceIDs = []string{deviceID}
	network.ProxyNetworks = []ProxyNetwork{{CIDR: "192.168.10.0/24", MappedCIDR: "10.10.0.0/24", Allow: []string{"TCP", "udp", "icmp"}}}
	state.Networks["company"] = network
	user := state.Users[0]

	clientConfig, err := buildRuntimeConfig(user, Device{ID: "44444444-4444-4444-8444-444444444444", VirtualIPs: map[string]string{"company": "10.144.0.10"}}, state.Networks)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clientConfig.EasyTierConfig, "[[proxy_network]]") {
		t.Fatal("ordinary client must not receive subnet proxy configuration")
	}
	if !strings.Contains(clientConfig.EasyTierConfig, "routes = []") {
		t.Fatalf("ordinary client must not receive local route entries:\n%s", clientConfig.EasyTierConfig)
	}
	if !strings.Contains(clientConfig.EasyTierConfig, `destination_ips = ["10.10.0.0/24"]`) {
		t.Fatalf("ordinary client ACL must use mapped access CIDR:\n%s", clientConfig.EasyTierConfig)
	}

	gatewayConfig, err := buildRuntimeConfig(user, Device{ID: deviceID, VirtualIPs: map[string]string{"company": "10.144.0.11"}}, state.Networks)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"[[proxy_network]]",
		`cidr = "192.168.10.0/24"`,
		`mapped_cidr = "10.10.0.0/24"`,
		`allow = ["tcp", "udp", "icmp"]`,
	} {
		if !strings.Contains(gatewayConfig.EasyTierConfig, expected) {
			t.Fatalf("gateway config missing %q:\n%s", expected, gatewayConfig.EasyTierConfig)
		}
	}
	if strings.Contains(gatewayConfig.EasyTierConfig, "[acl.acl_v1]") {
		t.Fatalf("gateway must not receive an end-user subnet ACL:\n%s", gatewayConfig.EasyTierConfig)
	}
}

func TestInvalidProxyNetworkRejected(t *testing.T) {
	state := defaultState()
	network := state.Networks["company"]
	network.ProxyNetworks = []ProxyNetwork{{CIDR: "not-a-cidr"}}
	state.Networks["company"] = network
	if err := validateState(state); err == nil || !strings.Contains(err.Error(), "invalid proxy cidr") {
		t.Fatalf("expected invalid proxy cidr error, got %v", err)
	}

	network.ProxyNetworks = []ProxyNetwork{{CIDR: "192.168.10.0/24", MappedCIDR: "not-a-cidr"}}
	state.Networks["company"] = network
	if err := validateState(state); err == nil || !strings.Contains(err.Error(), "invalid proxy mapped_cidr") {
		t.Fatalf("expected invalid proxy mapped_cidr error, got %v", err)
	}

	network.ProxyNetworks = []ProxyNetwork{{CIDR: "192.168.10.0/24", MappedCIDR: "10.10.0.0/25"}}
	state.Networks["company"] = network
	if err := validateState(state); err == nil || !strings.Contains(err.Error(), "same prefix length") {
		t.Fatalf("expected proxy prefix length error, got %v", err)
	}

	network.ProxyNetworks = []ProxyNetwork{{CIDR: "192.168.10.0/24"}, {CIDR: "192.168.10.0/24", MappedCIDR: "10.10.0.0/24"}}
	state.Networks["company"] = network
	if err := validateState(state); err == nil || !strings.Contains(err.Error(), "duplicate proxy network") {
		t.Fatalf("expected duplicate proxy network error, got %v", err)
	}
}

func TestUserSubnetScopeGeneratesForwardACL(t *testing.T) {
	state := defaultState()
	user := state.Users[0]
	user.AllowedSubnets = map[string][]string{"company": {"192.168.10.0/24"}}
	config, err := buildRuntimeConfig(user, Device{
		ID:         "66666666-6666-4666-8666-666666666666",
		VirtualIPs: map[string]string{"company": "10.144.0.10"},
	}, state.Networks)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"[acl.acl_v1]",
		"chain_type = 3",
		"default_action = 2",
		`destination_ips = ["192.168.10.0/24"]`,
	} {
		if !strings.Contains(config.EasyTierConfig, expected) {
			t.Fatalf("scoped client config missing %q:\n%s", expected, config.EasyTierConfig)
		}
	}
	if strings.Contains(config.EasyTierConfig, "192.168.20.0/24\"") {
		t.Fatal("scoped client config must not allow an unassigned subnet")
	}
}

func TestReloginInvalidatesPreviousDeviceSession(t *testing.T) {
	path := t.TempDir() + "/server.json"
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(store))
	defer server.Close()

	deviceID := "77777777-7777-4777-8777-777777777777"
	body := `{"username":"demo","password":"demo","device_id":"` + deviceID + `","platform":"test","client_version":"dev"}`
	firstResp, err := http.Post(server.URL+"/api/client/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var first ClientResponse
	if err := json.NewDecoder(firstResp.Body).Decode(&first); err != nil {
		t.Fatal(err)
	}
	_ = firstResp.Body.Close()
	secondResp, err := http.Post(server.URL+"/api/client/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var second ClientResponse
	if err := json.NewDecoder(secondResp.Body).Decode(&second); err != nil {
		t.Fatal(err)
	}
	_ = secondResp.Body.Close()
	if first.Token == second.Token {
		t.Fatal("relogin must issue a fresh token")
	}

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/client/heartbeat", strings.NewReader(`{"device_id":"`+deviceID+`"}`))
	req.Header.Set("Authorization", "Bearer "+first.Token)
	oldResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer oldResp.Body.Close()
	if oldResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old session status = %d", oldResp.StatusCode)
	}
}

func TestLogoutInvalidatesSession(t *testing.T) {
	path := t.TempDir() + "/server.json"
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(store))
	defer server.Close()

	deviceID := "99999999-9999-4999-8999-999999999999"
	body := `{"username":"demo","password":"demo","device_id":"` + deviceID + `","platform":"test","client_version":"dev"}`
	loginResp, err := http.Post(server.URL+"/api/client/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var login ClientResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&login); err != nil {
		_ = loginResp.Body.Close()
		t.Fatal(err)
	}
	_ = loginResp.Body.Close()

	logoutReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/client/logout", nil)
	logoutReq.Header.Set("Authorization", "Bearer "+login.Token)
	logoutResp, err := http.DefaultClient.Do(logoutReq)
	if err != nil {
		t.Fatal(err)
	}
	_ = logoutResp.Body.Close()
	if logoutResp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status = %d", logoutResp.StatusCode)
	}

	heartbeatReq, _ := http.NewRequest(http.MethodPost, server.URL+"/api/client/heartbeat", strings.NewReader(`{"device_id":"`+deviceID+`"}`))
	heartbeatReq.Header.Set("Authorization", "Bearer "+login.Token)
	heartbeatResp, err := http.DefaultClient.Do(heartbeatReq)
	if err != nil {
		t.Fatal(err)
	}
	defer heartbeatResp.Body.Close()
	if heartbeatResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("heartbeat after logout status = %d", heartbeatResp.StatusCode)
	}
}

func TestRuntimeInstanceIDIsStableAndNetworkScoped(t *testing.T) {
	left := runtimeInstanceID("device", "company")
	right := runtimeInstanceID("device", "finance")
	if left == right || left != runtimeInstanceID("device", "company") {
		t.Fatalf("runtime instance ids are not stable and scoped: %q %q", left, right)
	}
	if !validDeviceID(left) {
		t.Fatalf("runtime instance id is not a UUID: %q", left)
	}
}

func TestVirtualIPIsReallocatedWhenNetworkCIDRChanges(t *testing.T) {
	state := defaultState()
	user := state.Users[0]
	device := Device{ID: "88888888-8888-4888-8888-888888888888", VirtualIPs: map[string]string{"company": "10.144.0.10"}}
	state.Networks["company"] = Network{
		ID: "company", Name: "公司内网", Secret: "secret", VirtualCIDR: "10.200.0.0/16", Subnets: []string{"192.168.10.0/24"},
	}
	updated := ensureDeviceVirtualIPs(state, user, device)
	if updated.VirtualIPs["company"] == "10.144.0.10" || !ipv4InCIDR(updated.VirtualIPs["company"], "10.200.0.0/16") {
		t.Fatalf("virtual ip was not reallocated: %+v", updated.VirtualIPs)
	}

	state.Devices = map[string]Device{device.ID: device}
	store := &Store{data: state}
	store.normalizeDevicesLocked()
	repaired := store.data.Devices[device.ID]
	if !ipv4InCIDR(repaired.VirtualIPs["company"], "10.200.0.0/16") {
		t.Fatalf("state normalization did not repair virtual ip: %+v", repaired.VirtualIPs)
	}
}

func TestHeartbeatReloadsOperatorPermissionChanges(t *testing.T) {
	path := t.TempDir() + "/server.json"
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(store))
	defer server.Close()

	deviceID := "22222222-2222-4222-8222-222222222222"
	body := `{"username":"demo","password":"demo","device_id":"` + deviceID + `","platform":"test","client_version":"dev"}`
	loginResp, err := http.Post(server.URL+"/api/client/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var login ClientResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	_ = loginResp.Body.Close()
	if len(login.Config.AccessibleNetworks) != 2 {
		t.Fatalf("initial routes = %v", login.Config.AccessibleNetworks)
	}

	updated := defaultState()
	updated.Users[0].Revision = 2
	updated.Networks["company"] = Network{
		ID: "company", Name: "公司内网", Secret: "replace-this-network-secret", Gateway: "192.168.10.1",
		VirtualCIDR: "10.144.0.0/16", Subnets: []string{"192.168.10.0/24", "192.168.20.0/24", "192.168.30.0/24"},
	}
	updated.Devices = nil
	updated.Sessions = nil
	file, err := os.CreateTemp(filepath.Dir(path), "operator-state-*.json")
	if err != nil {
		t.Fatal(err)
	}
	encErr := json.NewEncoder(file).Encode(updated)
	closeErr := file.Close()
	if encErr != nil || closeErr != nil {
		t.Fatalf("write updated state: encode=%v close=%v", encErr, closeErr)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/client/heartbeat", strings.NewReader(`{"device_id":"`+deviceID+`"}`))
	req.Header.Set("Authorization", "Bearer "+login.Token)
	heartbeatResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer heartbeatResp.Body.Close()
	if heartbeatResp.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat status = %d", heartbeatResp.StatusCode)
	}
	var heartbeat ClientResponse
	if err := json.NewDecoder(heartbeatResp.Body).Decode(&heartbeat); err != nil {
		t.Fatal(err)
	}
	if heartbeat.Config.Revision == login.Config.Revision || len(heartbeat.Config.AccessibleNetworks) != 3 {
		t.Fatalf("updated routes/revision = %v/%s", heartbeat.Config.AccessibleNetworks, heartbeat.Config.Revision)
	}
}

func TestHeartbeatStopsWhenOperatorRevokesAllNetworks(t *testing.T) {
	path := t.TempDir() + "/server.json"
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(store))
	defer server.Close()

	deviceID := "55555555-5555-4555-8555-555555555555"
	body := `{"username":"demo","password":"demo","device_id":"` + deviceID + `","platform":"test","client_version":"dev"}`
	loginResp, err := http.Post(server.URL+"/api/client/login", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var login ClientResponse
	if err := json.NewDecoder(loginResp.Body).Decode(&login); err != nil {
		t.Fatal(err)
	}
	_ = loginResp.Body.Close()

	updated := defaultState()
	updated.Users[0].NetworkIDs = nil
	updated.Users[0].Revision = 2
	updated.Devices = nil
	updated.Sessions = nil
	file, err := os.CreateTemp(filepath.Dir(path), "operator-state-*.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(updated); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, server.URL+"/api/client/heartbeat", strings.NewReader(`{"device_id":"`+deviceID+`"}`))
	req.Header.Set("Authorization", "Bearer "+login.Token)
	heartbeatResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer heartbeatResp.Body.Close()
	if heartbeatResp.StatusCode != http.StatusForbidden {
		t.Fatalf("revoked heartbeat status = %d", heartbeatResp.StatusCode)
	}
}
