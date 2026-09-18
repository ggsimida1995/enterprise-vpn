package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
)

type Config struct {
	ServerURL string
	CorePath  string
	StatePath string
	UI        string
	Once      bool
}

type DeviceState struct {
	DeviceID string `json:"device_id"`
}

type LoginRequest struct {
	Username      string `json:"username"`
	Password      string `json:"password"`
	DeviceID      string `json:"device_id"`
	Platform      string `json:"platform"`
	ClientVersion string `json:"client_version"`
}

type HeartbeatRequest struct {
	DeviceID string `json:"device_id"`
}

type Response struct {
	Token string `json:"token"`
	User  struct {
		Username string `json:"username"`
	} `json:"user"`
	Config RuntimeConfig `json:"config"`
}

type RuntimeConfig struct {
	Revision           string   `json:"revision"`
	NetworkName        string   `json:"network_name"`
	AccessibleNetworks []string `json:"accessible_networks"`
	VirtualIP          string   `json:"virtual_ip"`
	EasyTierConfig     string   `json:"easytier_config"`
	EasyTierConfigs    []string `json:"easytier_configs,omitempty"`
}

type Client struct {
	config Config
	state  DeviceState
	mu     sync.Mutex
	cores  []*managedCore
	files  []string
}

type managedCore struct {
	cmd  *exec.Cmd
	done chan struct{}
}

type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("server returned HTTP %d: %s", e.status, e.message)
}

var httpClient = &http.Client{Timeout: 15 * time.Second}

var version = "dev"

var coreCommandArgs = func(file string) []string {
	return []string{"--config-file", file}
}

func main() {
	config := Config{}
	flag.StringVar(&config.ServerURL, "server", envOr("VPN_SERVER_URL", "http://127.0.0.1:8080"), "enterprise VPN server URL")
	flag.StringVar(&config.CorePath, "core", defaultCorePath(), "internal EasyTier core executable")
	flag.StringVar(&config.StatePath, "state", defaultStatePath(), "private client state path")
	flag.StringVar(&config.UI, "ui", envOr("VPN_UI", "browser"), "login interface: browser or cli")
	flag.BoolVar(&config.Once, "once", false, "login, start core and exit")
	flag.Parse()

	if err := run(config); err != nil {
		fmt.Fprintln(os.Stderr, "enterprise VPN:", err)
		os.Exit(1)
	}
}

func run(config Config) error {
	state, err := loadDeviceState(config.StatePath)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	username, password, err := readCredentialsForUI(ctx, config.UI)
	if err != nil {
		return err
	}
	client := &Client{config: config, state: state}

	var response Response
	for {
		response, err = client.login(ctx, username, password)
		if err == nil {
			break
		}
		var serverErr *apiError
		if browserUI(config.UI) && errors.As(err, &serverErr) && serverErr.status == http.StatusUnauthorized {
			fmt.Fprintln(os.Stderr, "账号或密码错误，请重新登录")
			username, password, err = readCredentialsForUI(ctx, config.UI)
			if err != nil {
				return err
			}
			continue
		}
		return err
	}
	if err := client.apply(response.Config); err != nil {
		return err
	}
	defer func() {
		_ = client.logout(context.Background(), response.Token)
	}()
	defer client.stopCore()
	fmt.Printf("\n企业内网\n\n● 已连接\n\n当前账号：%s\n当前网络：%s\n\n可访问：\n%s\n", response.User.Username, response.Config.NetworkName, strings.Join(response.Config.AccessibleNetworks, "\n"))
	if config.Once {
		return nil
	}

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			heartbeat, err := client.heartbeat(ctx, response.Token)
			if err != nil {
				var serverErr *apiError
				if errors.As(err, &serverErr) && (serverErr.status == http.StatusUnauthorized || serverErr.status == http.StatusForbidden) {
					client.stopCore()
					return err
				}
				if !client.coreHealthy() {
					if restartErr := client.apply(response.Config); restartErr != nil {
						fmt.Printf("网络服务暂时不可用，自动重连失败：%v\n", restartErr)
					} else {
						fmt.Println("网络服务已退出，正在自动重连")
					}
				}
				fmt.Printf("服务端暂时不可用，将继续重试：%v\n", err)
				continue
			}
			if heartbeat.Config.Revision != response.Config.Revision || !client.coreHealthy() {
				if err := client.apply(heartbeat.Config); err != nil {
					return err
				}
				if heartbeat.Config.Revision != response.Config.Revision {
					fmt.Println("网络权限已更新，连接已自动刷新")
				} else {
					fmt.Println("网络服务已退出，连接已自动恢复")
				}
			}
			response = heartbeat
		}
	}
}

func (c *Client) login(ctx context.Context, username, password string) (Response, error) {
	payload := LoginRequest{Username: username, Password: password, DeviceID: c.state.DeviceID, Platform: runtime.GOOS + "/" + runtime.GOARCH, ClientVersion: version}
	var response Response
	if err := c.post(ctx, "/api/client/login", "", payload, &response); err != nil {
		return Response{}, err
	}
	return response, nil
}

func (c *Client) heartbeat(ctx context.Context, token string) (Response, error) {
	var response Response
	if err := c.post(ctx, "/api/client/heartbeat", token, HeartbeatRequest{DeviceID: c.state.DeviceID}, &response); err != nil {
		return Response{}, err
	}
	response.Token = token
	return response, nil
}

func (c *Client) logout(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.ServerURL, "/")+"/api/client/logout", nil)
	if err != nil {
		return err
	}
	return c.do(req, token)
}

func (c *Client) post(ctx context.Context, path, token string, input, output any) error {
	body, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.config.ServerURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.doJSON(req, token, output)
}

func (c *Client) do(req *http.Request, token string) error {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &apiError{status: resp.StatusCode, message: strings.TrimSpace(string(message))}
	}
	return nil
}

func (c *Client) doJSON(req *http.Request, token string, output any) error {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &apiError{status: resp.StatusCode, message: strings.TrimSpace(string(message))}
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(output); err != nil {
		return err
	}
	return nil
}

func (c *Client) apply(config RuntimeConfig) error {
	configs := config.EasyTierConfigs
	if len(configs) == 0 && config.EasyTierConfig != "" {
		configs = []string{config.EasyTierConfig}
	}
	if len(configs) == 0 || config.Revision == "" {
		return errors.New("server returned incomplete network configuration")
	}
	c.stopCore()
	started := make([]*managedCore, 0, len(configs))
	files := make([]string, 0, len(configs))
	for i, content := range configs {
		file := filepath.Join(os.TempDir(), fmt.Sprintf("enterprise-vpn-%s-%d.toml", c.state.DeviceID, i))
		if err := os.WriteFile(file, []byte(content), 0600); err != nil {
			stopManagedCores(started, files)
			return err
		}
		cmd := exec.Command(c.config.CorePath, coreCommandArgs(file)...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Start(); err != nil {
			_ = os.Remove(file)
			stopManagedCores(started, files)
			return fmt.Errorf("start network service: %w", err)
		}
		core := &managedCore{cmd: cmd, done: make(chan struct{})}
		go func() {
			_ = cmd.Wait()
			close(core.done)
		}()
		select {
		case <-core.done:
			_ = os.Remove(file)
			stopManagedCores(started, files)
			return fmt.Errorf("network service exited during startup")
		default:
		}
		started = append(started, core)
		files = append(files, file)
	}
	c.mu.Lock()
	c.cores, c.files = started, files
	c.mu.Unlock()
	return nil
}

func (c *Client) coreHealthy() bool {
	c.mu.Lock()
	cores := append([]*managedCore(nil), c.cores...)
	c.mu.Unlock()
	if len(cores) == 0 {
		return false
	}
	for _, core := range cores {
		select {
		case <-core.done:
			return false
		default:
		}
	}
	return true
}

func (c *Client) stopCore() {
	c.mu.Lock()
	cores, files := c.cores, c.files
	c.cores, c.files = nil, nil
	c.mu.Unlock()
	stopManagedCores(cores, files)
}

func stopManagedCores(cores []*managedCore, files []string) {
	for i, core := range cores {
		if core == nil || core.cmd == nil || core.cmd.Process == nil {
			if i < len(files) {
				_ = os.Remove(files[i])
			}
			continue
		}
		_ = core.cmd.Process.Signal(os.Interrupt)
		select {
		case <-core.done:
		case <-time.After(3 * time.Second):
			_ = core.cmd.Process.Kill()
			<-core.done
		}
		if i < len(files) {
			_ = os.Remove(files[i])
		}
	}
}

func loadDeviceState(path string) (DeviceState, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		state := DeviceState{DeviceID: newDeviceID()}
		if dir := filepath.Dir(path); dir != "." {
			if err := os.MkdirAll(dir, 0700); err != nil {
				return DeviceState{}, err
			}
		}
		data, _ := json.MarshalIndent(state, "", "  ")
		if err := os.WriteFile(path, data, 0600); err != nil {
			return DeviceState{}, err
		}
		return state, nil
	}
	if err != nil {
		return DeviceState{}, err
	}
	var state DeviceState
	if err := json.Unmarshal(b, &state); err != nil || !validDeviceID(state.DeviceID) {
		return DeviceState{}, errors.New("invalid client state")
	}
	return state, nil
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

func newDeviceID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%s-%s-%s-%s-%s", hex.EncodeToString(b[0:4]), hex.EncodeToString(b[4:6]), hex.EncodeToString(b[6:8]), hex.EncodeToString(b[8:10]), hex.EncodeToString(b[10:16]))
}

func readCredentials() (string, string, error) {
	return readCredentialsFrom(os.Stdin, os.Stdout)
}

func readCredentialsFrom(input io.Reader, output io.Writer) (string, string, error) {
	reader := bufio.NewReader(input)
	fmt.Fprint(output, "企业内网\n\n账号: ")
	username, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", "", err
	}
	username = strings.TrimSpace(strings.TrimRight(username, "\r\n"))

	var password string
	if file, ok := input.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		fmt.Fprint(output, "密码: ")
		secret, readErr := term.ReadPassword(int(file.Fd()))
		fmt.Fprintln(output)
		if readErr != nil {
			return "", "", readErr
		}
		password = string(secret)
	} else {
		fmt.Fprint(output, "密码: ")
		password, err = reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", "", err
		}
		password = strings.TrimRight(password, "\r\n")
	}
	if username == "" || password == "" {
		return "", "", errors.New("账号和密码不能为空")
	}
	return username, password, nil
}

func defaultStatePath() string {
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "enterprise-vpn", "device.json")
	}
	return ".enterprise-vpn-device.json"
}

func defaultCorePath() string {
	executable, _ := os.Executable()
	return resolveCorePath(os.Getenv("VPN_CORE_PATH"), executable, runtime.GOOS == "windows")
}

func resolveCorePath(explicit, executable string, windows bool) string {
	if explicit != "" {
		return explicit
	}
	if executable != "" {
		names := []string{"easytier-core"}
		if windows {
			names = []string{"easytier-core.exe", "easytier-core"}
		}
		for _, name := range names {
			candidate := filepath.Join(filepath.Dir(executable), name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	if windows {
		return "easytier-core.exe"
	}
	return "easytier-core"
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
