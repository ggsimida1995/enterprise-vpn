package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

type AdminConfig struct {
	Users    []AdminUser        `json:"users"`
	Networks map[string]Network `json:"networks"`
}

type AdminUser struct {
	ID             string              `json:"id"`
	Username       string              `json:"username"`
	Password       string              `json:"password,omitempty"`
	NetworkIDs     []string            `json:"network_ids"`
	AllowedSubnets map[string][]string `json:"allowed_subnets,omitempty"`
	Revision       uint64              `json:"revision"`
}

type AdminStatus struct {
	Username           string `json:"username"`
	MustChangePassword bool   `json:"must_change_password"`
}

type AdminPasswordChangeRequest struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (s *Store) validAdminCredentials(username, password string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	admin := s.data.Admin
	usernameMatches := subtle.ConstantTimeCompare([]byte(username), []byte(admin.Username)) == 1
	passwordMatches := checkPassword(password, admin.PasswordHash, admin.PasswordSalt)
	return usernameMatches && passwordMatches
}

func (s *Store) adminStatus() AdminStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return AdminStatus{Username: s.data.Admin.Username, MustChangePassword: s.data.Admin.MustChangePassword}
}

func (s *Store) changeAdminPassword(currentPassword, newPassword string) error {
	if len(newPassword) < 8 {
		return errors.New("new admin password must be at least 8 characters")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	admin := &s.data.Admin
	if !checkPassword(currentPassword, admin.PasswordHash, admin.PasswordSalt) {
		return errors.New("current admin password is invalid")
	}
	previous := *admin
	salt := randomToken()[:32]
	admin.PasswordSalt = salt
	admin.PasswordHash = hashPassword(newPassword, salt)
	admin.MustChangePassword = false
	if err := s.persistLocked(); err != nil {
		s.data.Admin = previous
		return err
	}
	return nil
}

func (s *Store) adminConfig() AdminConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, err := cloneState(s.data)
	if err != nil {
		return AdminConfig{}
	}
	users := make([]AdminUser, 0, len(state.Users))
	for _, user := range state.Users {
		users = append(users, AdminUser{ID: user.ID, Username: user.Username, NetworkIDs: user.NetworkIDs, AllowedSubnets: user.AllowedSubnets, Revision: user.Revision})
	}
	return AdminConfig{Users: users, Networks: state.Networks}
}

func (s *Store) updateAdminConfig(input AdminConfig) error {
	if len(input.Users) == 0 || len(input.Networks) == 0 {
		return errors.New("users and networks are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	previous, err := cloneState(s.data)
	if err != nil {
		return fmt.Errorf("snapshot server state: %w", err)
	}
	oldUsers := make(map[string]User, len(previous.Users))
	for _, user := range previous.Users {
		oldUsers[user.ID] = user
	}
	users := make([]User, 0, len(input.Users))
	for _, inputUser := range input.Users {
		if inputUser.ID == "" || inputUser.Username == "" {
			return errors.New("each user needs id and username")
		}
		user := oldUsers[inputUser.ID]
		user.ID = inputUser.ID
		user.Username = strings.TrimSpace(inputUser.Username)
		user.NetworkIDs = inputUser.NetworkIDs
		user.AllowedSubnets = inputUser.AllowedSubnets
		if inputUser.Password != "" {
			user.PasswordSalt = randomToken()[:32]
			user.PasswordHash = hashPassword(inputUser.Password, user.PasswordSalt)
		}
		if user.PasswordHash == "" || user.PasswordSalt == "" {
			return fmt.Errorf("user %q needs a password on first save", user.Username)
		}
		user.Revision++
		if user.Revision == 0 {
			user.Revision = 1
		}
		users = append(users, user)
	}
	updated := State{Admin: previous.Admin, Users: users, Networks: input.Networks, Devices: previous.Devices, Sessions: previous.Sessions}
	updatedStore := &Store{data: updated}
	updatedStore.normalizeDevicesLocked()
	if err := validateState(updated); err != nil {
		return err
	}
	s.data = updated
	if err := s.persistLocked(); err != nil {
		s.data = previous
		return err
	}
	return nil
}

func adminProtected(store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || !store.validAdminCredentials(username, password) {
			w.Header().Set("WWW-Authenticate", `Basic realm="enterprise-vpn-admin"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func adminPasswordChanged(store *Store, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if store.adminStatus().MustChangePassword {
			writeError(w, http.StatusForbidden, "change the initial admin password first")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func adminRoutes(store *Store) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /admin", adminProtected(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(adminPage))
	})))
	mux.Handle("GET /api/admin/status", adminProtected(store, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, store.adminStatus())
	})))
	mux.Handle("POST /api/admin/password", adminProtected(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input AdminPasswordChangeRequest
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := store.changeAdminPassword(input.CurrentPassword, input.NewPassword); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, store.adminStatus())
	})))
	config := adminPasswordChanged(store, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, store.adminConfig())
			return
		}
		var input AdminConfig
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := store.updateAdminConfig(input); err != nil {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, store.adminConfig())
	}))
	mux.Handle("GET /api/admin/config", adminProtected(store, config))
	mux.Handle("PUT /api/admin/config", adminProtected(store, config))
	return mux
}

var adminPage = `<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>企业内网服务端配置</title>
<style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;margin:32px;max-width:1100px;color:#17212b}h1{font-size:24px}p{color:#52606d}label{display:block;margin:18px 0 6px;font-weight:600}input,textarea{box-sizing:border-box;width:100%;padding:12px;border:1px solid #b9c5cf;border-radius:5px;font:14px ui-monospace,SFMono-Regular,Menlo,monospace}textarea{min-height:220px}button{margin-top:16px;padding:10px 18px;border:0;border-radius:5px;background:#1769aa;color:white;font-weight:600;cursor:pointer}#status{margin-left:12px}</style></head>
<body><h1>企业内网服务端配置</h1><p>用户权限和 EasyTier 网络参数由服务端统一下发。</p>
<section id="password-section" hidden><p>首次登录必须先修改后台密码，修改后才能编辑配置。</p><form onsubmit="changePassword(event)"><label for="current-password">当前密码</label><input id="current-password" type="password" autocomplete="current-password" required><label for="new-password">新密码</label><input id="new-password" type="password" minlength="8" autocomplete="new-password" required><button type="submit">修改后台密码</button></form></section>
<section id="config-section" hidden><p>用户密码留空表示保留当前密码。</p><label for="users">用户配置 JSON</label><textarea id="users"></textarea><label for="networks">网络配置 JSON</label><textarea id="networks"></textarea><button onclick="save()">保存配置</button></section><span id="status"></span>
<script>
async function load(){const status=document.getElementById('status');const r=await fetch('/api/admin/status');if(!r.ok){status.textContent='加载失败';return}const state=await r.json();if(state.must_change_password){document.getElementById('password-section').hidden=false;return}document.getElementById('config-section').hidden=false;const config=await fetch('/api/admin/config');if(!config.ok){status.textContent='加载失败';return}const d=await config.json();document.getElementById('users').value=JSON.stringify(d.users,null,2);document.getElementById('networks').value=JSON.stringify(d.networks,null,2)}
async function changePassword(event){event.preventDefault();const status=document.getElementById('status');try{const r=await fetch('/api/admin/password',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({current_password:document.getElementById('current-password').value,new_password:document.getElementById('new-password').value})});const d=await r.json();if(!r.ok)throw new Error(d.error||'修改失败');status.textContent='密码已修改，请刷新页面并在登录提示框中输入新密码'}catch(e){status.textContent=e.message}}
async function save(){const status=document.getElementById('status');try{const body={users:JSON.parse(document.getElementById('users').value),networks:JSON.parse(document.getElementById('networks').value)};const r=await fetch('/api/admin/config',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});const d=await r.json();if(!r.ok)throw new Error(d.error||'保存失败');document.getElementById('users').value=JSON.stringify(d.users,null,2);document.getElementById('networks').value=JSON.stringify(d.networks,null,2);status.textContent='已保存'}catch(e){status.textContent=e.message}}
load();
</script></body></html>`
