package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestDeviceStateIsStable(t *testing.T) {
	path := t.TempDir() + "/device.json"
	first, err := loadDeviceState(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadDeviceState(path)
	if err != nil {
		t.Fatal(err)
	}
	if first.DeviceID == "" || first.DeviceID != second.DeviceID {
		t.Fatalf("device id is not stable: %+v %+v", first, second)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("state permissions = %v, err = %v", info.Mode().Perm(), err)
	}
}

func TestCoreHealthyDetectsExit(t *testing.T) {
	client := &Client{cores: []*managedCore{{done: make(chan struct{})}}}
	if !client.coreHealthy() {
		t.Fatal("running core should be healthy")
	}
	close(client.cores[0].done)
	if client.coreHealthy() {
		t.Fatal("exited core should be unhealthy")
	}
}

func TestDeviceStateRejectsTamperedID(t *testing.T) {
	path := t.TempDir() + "/device.json"
	if err := os.WriteFile(path, []byte(`{"device_id":"not-a-uuid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDeviceState(path); err == nil {
		t.Fatal("tampered device id should be rejected")
	}
}

func TestReadCredentialsPreservesPasswordSpaces(t *testing.T) {
	username, password, err := readCredentialsFrom(bytes.NewBufferString(" demo \n pass word  \n"), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if username != "demo" || password != " pass word  " {
		t.Fatalf("credentials = %q/%q", username, password)
	}
}

func TestBrowserLoginHandler(t *testing.T) {
	credentials := make(chan loginCredentials, 1)
	server := httptest.NewServer(browserLoginHandler(credentials, "test-token"))
	defer server.Close()

	response, err := http.Get(server.URL + "/login/test-token")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), "企业内网") {
		t.Fatalf("login page status/body = %d/%q", response.StatusCode, body)
	}

	form := url.Values{"username": {"demo"}, "password": {" pass word  "}}
	response, err = http.PostForm(server.URL+"/login/test-token", form)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login submit status = %d", response.StatusCode)
	}
	select {
	case got := <-credentials:
		if got.username != "demo" || got.password != " pass word  " {
			t.Fatalf("credentials = %q/%q", got.username, got.password)
		}
	default:
		t.Fatal("login credentials were not submitted")
	}
}

func TestResolveCorePath(t *testing.T) {
	if got := resolveCorePath("/custom/easytier-core", "/app/client", false); got != "/custom/easytier-core" {
		t.Fatalf("explicit core path = %q", got)
	}
	dir := t.TempDir()
	executable := dir + "/enterprise-vpn-client"
	if err := os.WriteFile(dir+"/easytier-core", []byte("core"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := resolveCorePath("", executable, false); got != dir+"/easytier-core" {
		t.Fatalf("bundled core path = %q", got)
	}
	windowsDir := t.TempDir()
	if got := resolveCorePath("", windowsDir+"/missing-client", true); got != "easytier-core.exe" {
		t.Fatalf("windows fallback core path = %q", got)
	}
}

func TestApplyStartsAndStopsCore(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("shell is unavailable")
	}
	client := &Client{
		config: Config{CorePath: "sh"},
		state:  DeviceState{DeviceID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"},
	}
	config := RuntimeConfig{
		Revision:       "revision-1",
		EasyTierConfig: "network_name = \"test\"\n",
	}

	// The helper shell stays alive until the client sends its interrupt signal.
	originalArgs := coreCommandArgs
	coreCommandArgs = func(file string) []string {
		return []string{"-c", "trap 'exit 0' INT TERM; while :; do sleep 1; done", "enterprise-vpn", file}
	}
	defer func() { coreCommandArgs = originalArgs }()

	if err := client.apply(config); err != nil {
		t.Fatal(err)
	}
	if !client.coreHealthy() || len(client.files) != 1 {
		t.Fatalf("core was not started: healthy=%v files=%v", client.coreHealthy(), client.files)
	}
	file := client.files[0]
	if content, err := os.ReadFile(file); err != nil || !strings.Contains(string(content), "network_name") {
		t.Fatalf("temporary config = %q, err = %v", content, err)
	}
	client.stopCore()
	if client.coreHealthy() {
		t.Fatal("core should be stopped")
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("temporary config still exists, err = %v", err)
	}
}
