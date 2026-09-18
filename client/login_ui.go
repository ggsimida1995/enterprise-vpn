package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type loginCredentials struct {
	username string
	password string
}

var loginPageTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>企业内网</title>
  <style>
    :root { color-scheme: light; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: #f4f6f8; color: #17212b; }
    main { width: min(360px, calc(100vw - 40px)); padding: 32px; background: #fff; border: 1px solid #d9e0e6; border-radius: 8px; box-shadow: 0 12px 32px rgba(20, 38, 55, .08); }
    h1 { margin: 0 0 24px; font-size: 24px; font-weight: 650; }
    label { display: block; margin: 16px 0 6px; font-size: 14px; color: #52606d; }
    input { box-sizing: border-box; width: 100%; padding: 11px 12px; border: 1px solid #b9c5cf; border-radius: 5px; font: inherit; }
    input:focus { outline: 2px solid #8ec5ff; outline-offset: 1px; border-color: #2672c9; }
    button { width: 100%; margin-top: 24px; padding: 11px 12px; border: 0; border-radius: 5px; background: #1769aa; color: #fff; font: inherit; font-weight: 600; cursor: pointer; }
    button:hover { background: #12588e; }
    .message { min-height: 20px; margin: -8px 0 8px; color: #b42318; font-size: 14px; }
  </style>
</head>
<body>
  <main>
    <h1>企业内网</h1>
	<form method="post" action="{{.Action}}">
      <label for="username">账号</label>
      <input id="username" name="username" autocomplete="username" required autofocus>
      <label for="password">密码</label>
      <input id="password" name="password" type="password" autocomplete="current-password" required>
      <p class="message">{{.Message}}</p>
      <button type="submit">登录</button>
    </form>
  </main>
</body>
</html>`))

var loggedInPageTemplate = template.Must(template.New("logged-in").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>企业内网</title></head>
<body style='font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;display:grid;place-items:center;min-height:100vh;color:#17212b'><p>登录请求已提交，可以关闭此页面。</p></body></html>`))

func readCredentialsForUI(ctx context.Context, mode string) (string, string, error) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "browser", "web":
		return readCredentialsBrowser(ctx)
	case "cli", "terminal":
		return readCredentials()
	default:
		return "", "", fmt.Errorf("unsupported login interface %q (use browser or cli)", mode)
	}
}

func browserUI(mode string) bool {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", "browser", "web":
		return true
	default:
		return false
	}
}

func readCredentialsBrowser(ctx context.Context) (string, string, error) {
	credentials := make(chan loginCredentials, 1)
	token, err := newLoginToken()
	if err != nil {
		return "", "", err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", fmt.Errorf("start local login page: %w", err)
	}
	server := &http.Server{Handler: browserLoginHandler(credentials, token)}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
		_ = listener.Close()
	}()

	url := "http://" + listener.Addr().String() + "/login/" + token
	if err := openBrowser(url); err != nil {
		fmt.Fprintf(os.Stderr, "无法自动打开登录页面，请手动访问 %s (%v)\n", url, err)
	} else {
		fmt.Fprintln(os.Stdout, "登录页面已打开")
	}

	select {
	case value := <-credentials:
		return value.username, value.password, nil
	case <-ctx.Done():
		return "", "", ctx.Err()
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return "", "", fmt.Errorf("login page stopped: %w", err)
		}
		return "", "", errors.New("login page stopped")
	}
}

func browserLoginHandler(credentials chan<- loginCredentials, token string) http.Handler {
	mux := http.NewServeMux()
	loginPath := "/login/" + token
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		http.Redirect(w, r, loginPath, http.StatusFound)
	})
	mux.HandleFunc(loginPath, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeLoginPage(w, "", loginPath)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			writeLoginPage(w, "登录信息无效", loginPath)
			return
		}
		username := strings.TrimSpace(r.FormValue("username"))
		password := r.FormValue("password")
		if username == "" || password == "" {
			writeLoginPage(w, "账号和密码不能为空", loginPath)
			return
		}
		select {
		case credentials <- loginCredentials{username: username, password: password}:
		default:
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = loggedInPageTemplate.Execute(w, nil)
	})
	return mux
}

func writeLoginPage(w http.ResponseWriter, message, action string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = loginPageTemplate.Execute(w, struct {
		Message string
		Action  string
	}{Message: message, Action: action})
}

func newLoginToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("create login page token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func openBrowser(url string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command, args = "open", []string{url}
	case "windows":
		command, args = "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		command, args = "xdg-open", []string{url}
	}
	return exec.Command(command, args...).Start()
}
