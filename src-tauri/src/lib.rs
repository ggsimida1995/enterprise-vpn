#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use anyhow::{Context, Result, anyhow};
use directories::ProjectDirs;
use futures_util::{SinkExt, StreamExt};
use serde::{Deserialize, Serialize};
use serde_json::{Value, json};
use std::{
    fs,
    io::Write,
    net::TcpListener,
    path::{Path, PathBuf},
    process::{Child, Command},
    sync::{Arc, Mutex},
    time::{Duration, SystemTime},
};
use tauri::{Emitter, Manager};
use tokio::time::{self, MissedTickBehavior};
use tokio_tungstenite::{connect_async, tungstenite::Message};
use url::Url;
use uuid::Uuid;

const DEFAULT_SERVER_URL: &str = match option_env!("VPN_SERVER_URL") {
    Some(value) => value,
    None => "http://172.22.159.5:4096",
};
const CLIENT_VERSION: &str = concat!("tauri-", env!("CARGO_PKG_VERSION"));
const CONNECTION_TIMEOUT_SECS: u64 = 10;
const RESPONSE_TIMEOUT_SECS: u64 = 15;

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
struct DeviceState {
    device_id: String,
    #[serde(default)]
    device_token: String,
    #[serde(default)]
    username: String,
    #[serde(default = "default_server_url")]
    server_url: String,
}

fn default_server_url() -> String {
    DEFAULT_SERVER_URL.to_owned()
}

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
struct RuntimeConfig {
    #[serde(default)]
    revision: String,
    #[serde(rename = "profileName", default)]
    network_name: String,
    #[serde(default)]
    config: String,
}

#[derive(Clone, Copy)]
enum LoginStage {
    Connecting,
    Authenticating,
    FetchingConfig,
    StartingCore,
    Ready,
}

impl LoginStage {
    fn step(self) -> u8 {
        match self {
            Self::Connecting => 0,
            Self::Authenticating => 1,
            Self::FetchingConfig => 2,
            Self::StartingCore => 3,
            Self::Ready => 4,
        }
    }

    fn message(self) -> &'static str {
        match self {
            Self::Connecting => "正在连接服务端…",
            Self::Authenticating => "正在验证账号…",
            Self::FetchingConfig => "登录成功，正在获取客户端配置…",
            Self::StartingCore => "配置获取成功，正在启动网络服务…",
            Self::Ready => "网络服务已启动，正在完成连接…",
        }
    }
}

#[derive(Debug, Clone, Deserialize)]
struct AgentMessage {
    #[serde(default)]
    event: String,
    #[serde(default)]
    data: Value,
    #[serde(default)]
    code: i32,
    #[serde(rename = "errorMsg", default)]
    error_msg: String,
}

#[derive(Debug, Serialize)]
struct LoginResult {
    username: String,
    network_name: String,
}

#[derive(Debug, Clone, Serialize)]
struct ClientConfig {
    loaded: bool,
    network_name: String,
    revision: String,
    config: String,
}

#[derive(Debug, Clone, Serialize)]
struct ClientLogs {
    client: String,
    easytier: String,
}

#[derive(Debug, Clone, Serialize)]
struct EasyTierPeer {
    id: String,
    hostname: String,
    virtual_ipv4: String,
    protocol: String,
    latency_ms: Option<f64>,
    loss_rate: Option<f32>,
    local: bool,
}

#[derive(Debug, Clone, Serialize)]
struct EasyTierStatus {
    running: bool,
    vpn_enabled: bool,
    version: String,
    virtual_ipv4: String,
    nat_type: String,
    port_range: String,
    rx_bytes: u64,
    tx_bytes: u64,
    peers: Vec<EasyTierPeer>,
    server_connected: bool,
    authenticated: bool,
    config_loaded: bool,
    core_started: bool,
    rpc_available: bool,
    network_name: String,
    revision: String,
    last_error: Option<String>,
}

struct SessionState {
    stop: Option<tokio::sync::oneshot::Sender<()>>,
    vpn_control: Option<tokio::sync::watch::Sender<bool>>,
    vpn_enabled: bool,
    rpc_port: Option<u16>,
    username: String,
    runtime_config: Option<RuntimeConfig>,
    server_connected: bool,
    authenticated: bool,
    config_loaded: bool,
    core_started: bool,
    last_error: Option<String>,
}

impl Default for SessionState {
    fn default() -> Self {
        Self {
            stop: None,
            vpn_control: None,
            vpn_enabled: true,
            rpc_port: None,
            username: String::new(),
            runtime_config: None,
            server_connected: false,
            authenticated: false,
            config_loaded: false,
            core_started: false,
            last_error: None,
        }
    }
}

#[tauri::command]
async fn login(
    app: tauri::AppHandle,
    username: String,
    password: String,
    state: tauri::State<'_, Arc<Mutex<SessionState>>>,
) -> Result<LoginResult, String> {
    let username = username.trim().to_owned();
    if username.is_empty() || password.is_empty() {
        client_log("login rejected locally: empty username or password");
        return Err("账号和密码不能为空".to_owned());
    }
    let path = state_path().map_err(|error| error.to_string())?;
    let saved = load_state(&path).map_err(|error| error.to_string())?;
    client_log(&format!(
        "login requested username={username} endpoint={}",
        saved.server_url
    ));

    start_session(
        app,
        username,
        password,
        false,
        saved.server_url,
        state.inner().clone(),
    )
    .await
}

#[tauri::command]
async fn restore_session(
    app: tauri::AppHandle,
    state: tauri::State<'_, Arc<Mutex<SessionState>>>,
) -> Result<LoginResult, String> {
    let path = state_path().map_err(|error| error.to_string())?;
    let saved = load_state(&path).map_err(|error| error.to_string())?;
    if saved.device_token.is_empty() || saved.username.is_empty() {
        return Err("没有可恢复的登录状态".to_owned());
    }
    client_log(&format!(
        "restoring login username={} endpoint={}",
        saved.username, saved.server_url
    ));
    start_session(
        app,
        saved.username,
        String::new(),
        true,
        saved.server_url,
        state.inner().clone(),
    )
    .await
}

async fn start_session(
    app: tauri::AppHandle,
    username: String,
    password: String,
    restore: bool,
    server_url: String,
    session_state: Arc<Mutex<SessionState>>,
) -> Result<LoginResult, String> {
    let (stop_tx, stop_rx) = tokio::sync::oneshot::channel();
    let (vpn_tx, vpn_rx) = tokio::sync::watch::channel(true);
    let (ready_tx, ready_rx) = tokio::sync::oneshot::channel();
    {
        let mut state = session_state
            .lock()
            .map_err(|_| "客户端状态不可用".to_owned())?;
        if let Some(stop) = state.stop.take() {
            let _ = stop.send(());
        }
        state.vpn_control = Some(vpn_tx);
        state.vpn_enabled = true;
        state.stop = Some(stop_tx);
        state.rpc_port = None;
        state.username = username.clone();
        state.runtime_config = None;
        state.server_connected = false;
        state.authenticated = false;
        state.config_loaded = false;
        state.core_started = false;
        state.last_error = None;
    }

    tokio::spawn(async move {
        let result = session(
            app,
            username,
            password,
            restore,
            server_url,
            stop_rx,
            vpn_rx,
            ready_tx,
            session_state.clone(),
        )
        .await;
        if let Err(error) = result {
            if let Ok(mut state) = session_state.lock() {
                state.rpc_port = None;
                state.vpn_control = None;
                state.vpn_enabled = false;
                state.server_connected = false;
                state.authenticated = false;
                state.config_loaded = false;
                state.core_started = false;
                state.last_error = Some(error.to_string());
            }
            eprintln!("client session stopped: {error:#}");
        }
    });

    ready_rx
        .await
        .map_err(|_| "客户端会话启动失败".to_owned())?
        .map_err(|error| error.to_string())
}

#[tauri::command]
fn get_service_url() -> Result<String, String> {
    let path = state_path().map_err(|error| error.to_string())?;
    load_state(&path)
        .map(|state| state.server_url)
        .map_err(|error| error.to_string())
}

#[tauri::command]
fn set_service_url(server_url: String) -> Result<String, String> {
    let server_url = normalize_server_url(&server_url).map_err(|error| error.to_string())?;
    let path = state_path().map_err(|error| error.to_string())?;
    let mut state = load_state(&path).map_err(|error| error.to_string())?;
    state.server_url = server_url.clone();
    save_state(&path, &state).map_err(|error| error.to_string())?;
    client_log(&format!("service endpoint updated endpoint={server_url}"));
    Ok(server_url)
}

#[tauri::command]
fn logout(state: tauri::State<'_, Arc<Mutex<SessionState>>>) -> Result<(), String> {
    client_log("logout requested");
    let mut state = state.lock().map_err(|_| "客户端状态不可用".to_owned())?;
    if let Some(stop) = state.stop.take() {
        let _ = stop.send(());
    }
    state.vpn_control = None;
    state.vpn_enabled = false;
    state.rpc_port = None;
    state.runtime_config = None;
    state.server_connected = false;
    state.authenticated = false;
    state.config_loaded = false;
    state.core_started = false;
    if let Ok(path) = state_path() {
        if let Ok(mut saved) = load_state(&path) {
            saved.device_token.clear();
            saved.username.clear();
            let _ = save_state(&path, &saved);
        }
    }
    Ok(())
}

#[tauri::command]
fn set_vpn_enabled(
    enabled: bool,
    state: tauri::State<'_, Arc<Mutex<SessionState>>>,
) -> Result<(), String> {
    let control = {
        let state = state.lock().map_err(|_| "客户端状态不可用".to_owned())?;
        state
            .vpn_control
            .clone()
            .ok_or_else(|| "VPN 会话尚未启动".to_owned())?
    };
    control
        .send(enabled)
        .map_err(|_| "VPN 会话已结束".to_owned())?;
    client_log(if enabled {
        "VPN start requested"
    } else {
        "VPN stop requested"
    });
    Ok(())
}

#[tauri::command]
fn get_client_config(
    state: tauri::State<'_, Arc<Mutex<SessionState>>>,
) -> Result<ClientConfig, String> {
    let state = state.lock().map_err(|_| "客户端状态不可用".to_owned())?;
    let Some(config) = state.runtime_config.as_ref() else {
        return Ok(ClientConfig {
            loaded: false,
            network_name: String::new(),
            revision: String::new(),
            config: String::new(),
        });
    };
    Ok(ClientConfig {
        loaded: true,
        network_name: config.network_name.clone(),
        revision: config.revision.clone(),
        config: config.config.clone(),
    })
}

#[tauri::command]
fn get_client_logs() -> Result<ClientLogs, String> {
    let path = state_path().map_err(|error| error.to_string())?;
    Ok(ClientLogs {
        client: read_log_tail(&path.with_file_name("client.log")),
        easytier: read_log_tail(&path.with_file_name("easytier-core.log")),
    })
}

#[tauri::command]
fn clear_log(kind: String) -> Result<(), String> {
    let file_name = match kind.as_str() {
        "client" => "client.log",
        "easytier" => "easytier-core.log",
        _ => return Err("不支持的日志类型".to_owned()),
    };
    let path = state_path()
        .map_err(|error| error.to_string())?
        .with_file_name(file_name);
    fs::write(path, b"").map_err(|error| error.to_string())
}

#[tauri::command]
async fn get_easytier_status(
    app: tauri::AppHandle,
    state: tauri::State<'_, Arc<Mutex<SessionState>>>,
) -> Result<EasyTierStatus, String> {
    let (
        rpc_port,
        server_connected,
        authenticated,
        config_loaded,
        core_started,
        vpn_enabled,
        network_name,
        revision,
        last_error,
    ) = {
        let state = state.lock().map_err(|_| "客户端状态不可用".to_owned())?;
        (
            state.rpc_port,
            state.server_connected,
            state.authenticated,
            state.config_loaded,
            state.core_started,
            state.vpn_enabled,
            state
                .runtime_config
                .as_ref()
                .map(|config| config.network_name.clone())
                .unwrap_or_default(),
            state
                .runtime_config
                .as_ref()
                .map(|config| config.revision.clone())
                .unwrap_or_default(),
            state.last_error.clone(),
        )
    };
    let Some(rpc_port) = rpc_port else {
        return Ok(EasyTierStatus::offline_with_client_state(
            server_connected,
            authenticated,
            config_loaded,
            core_started,
            vpn_enabled,
            network_name,
            revision,
            last_error,
        ));
    };

    match time::timeout(
        Duration::from_secs(3),
        query_easytier_status(&app, rpc_port),
    )
    .await
    {
        Ok(Ok(mut status)) => {
            status.server_connected = server_connected;
            status.authenticated = authenticated;
            status.config_loaded = config_loaded;
            status.core_started = core_started;
            status.vpn_enabled = vpn_enabled;
            status.rpc_available = true;
            status.network_name = network_name;
            status.revision = revision;
            status.last_error = last_error;
            Ok(status)
        }
        Ok(Err(error)) => {
            client_log(&format!("EasyTier status unavailable: {error}"));
            Ok(EasyTierStatus::offline_with_client_state(
                server_connected,
                authenticated,
                config_loaded,
                core_started,
                vpn_enabled,
                network_name,
                revision,
                Some(error.to_string()),
            ))
        }
        Err(_) => {
            client_log("EasyTier status query timed out");
            Ok(EasyTierStatus::offline_with_client_state(
                server_connected,
                authenticated,
                config_loaded,
                core_started,
                vpn_enabled,
                network_name,
                revision,
                Some("读取 EasyTier 状态超时".to_owned()),
            ))
        }
    }
}

#[tauri::command]
fn close_client(state: tauri::State<'_, Arc<Mutex<SessionState>>>) -> Result<(), String> {
    set_vpn_enabled(false, state)
}

#[tauri::command]
fn set_window_mode(app: tauri::AppHandle, connected: bool) -> Result<(), String> {
    let window = app
        .get_webview_window("main")
        .ok_or_else(|| "客户端窗口不可用".to_owned())?;
    let (width, height) = if connected {
        (520.0, 760.0)
    } else {
        (520.0, 540.0)
    };
    window
        .set_size(tauri::Size::Logical(tauri::LogicalSize::new(width, height)))
        .map_err(|error| error.to_string())?;
    window.center().map_err(|error| error.to_string())?;
    Ok(())
}

impl EasyTierStatus {
    fn offline_with_client_state(
        server_connected: bool,
        authenticated: bool,
        config_loaded: bool,
        core_started: bool,
        vpn_enabled: bool,
        network_name: String,
        revision: String,
        last_error: Option<String>,
    ) -> Self {
        Self {
            running: false,
            vpn_enabled,
            version: String::new(),
            virtual_ipv4: String::new(),
            nat_type: String::new(),
            port_range: String::new(),
            rx_bytes: 0,
            tx_bytes: 0,
            peers: Vec::new(),
            server_connected,
            authenticated,
            config_loaded,
            core_started,
            rpc_available: false,
            network_name,
            revision,
            last_error,
        }
    }
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .manage(Arc::new(Mutex::new(SessionState::default())))
        .invoke_handler(tauri::generate_handler![
            login,
            restore_session,
            logout,
            set_vpn_enabled,
            get_service_url,
            set_service_url,
            get_easytier_status,
            close_client,
            set_window_mode,
            get_client_config,
            get_client_logs,
            clear_log
        ])
        .run(tauri::generate_context!())
        .expect("error while running enterprise-vpn-client");
}

async fn session(
    app: tauri::AppHandle,
    username: String,
    password: String,
    restore: bool,
    server_url: String,
    mut stop_rx: tokio::sync::oneshot::Receiver<()>,
    mut vpn_rx: tokio::sync::watch::Receiver<bool>,
    ready_tx: tokio::sync::oneshot::Sender<Result<LoginResult>>,
    session_state: Arc<Mutex<SessionState>>,
) -> Result<()> {
    let mut ready_tx = Some(ready_tx);
    client_log(&format!("session started endpoint={server_url}"));
    emit_login_status(&app, LoginStage::Connecting);
    let state_path = match state_path() {
        Ok(path) => path,
        Err(error) => {
            report_ready_error(&mut ready_tx, &error);
            return Err(error);
        }
    };
    let mut state = match load_state(&state_path) {
        Ok(state) => state,
        Err(error) => {
            report_ready_error(&mut ready_tx, &error);
            return Err(error);
        }
    };
    let agent = match agent_url(&server_url) {
        Ok(agent) => agent,
        Err(error) => {
            report_ready_error(&mut ready_tx, &error);
            return Err(error);
        }
    };
    let mut socket = match time::timeout(
        Duration::from_secs(CONNECTION_TIMEOUT_SECS),
        connect_async(agent.as_str()),
    )
    .await
    {
        Ok(Ok((socket, _))) => socket,
        Ok(Err(error)) => {
            let error: anyhow::Error = error.into();
            client_log(&format!("websocket connect failed: {error}"));
            report_ready_error(&mut ready_tx, &error);
            return Err(error);
        }
        Err(_) => {
            let error = anyhow!(
                "连接服务端超时（{} 秒）：{}",
                CONNECTION_TIMEOUT_SECS,
                server_url
            );
            client_log(&format!("websocket connect timeout: {error}"));
            report_ready_error(&mut ready_tx, &error);
            return Err(error);
        }
    };
    client_log("websocket connected");
    if let Ok(mut session_state) = session_state.lock() {
        session_state.server_connected = true;
    }
    let event = if restore {
        json!({
            "event": "auth.reconnect",
            "data": {
                "deviceId": state.device_id,
                "deviceToken": state.device_token,
                "platform": format!("{}-{}", std::env::consts::OS, std::env::consts::ARCH),
                "version": CLIENT_VERSION
            }
        })
    } else {
        json!({
            "event": "auth.login",
            "data": {
                "username": username,
                "password": password,
                "deviceId": state.device_id,
                "platform": format!("{}-{}", std::env::consts::OS, std::env::consts::ARCH),
                "version": CLIENT_VERSION
            }
        })
    };
    client_log(&format!(
        "send event={}",
        event["event"].as_str().unwrap_or("unknown")
    ));
    if let Err(error) = socket.send(Message::Text(event.to_string().into())).await {
        let error: anyhow::Error = error.into();
        client_log(&format!("websocket send failed: {error}"));
        report_ready_error(&mut ready_tx, &error);
        return Err(error);
    }

    emit_login_status(&app, LoginStage::Authenticating);
    let mut username_from_server = username.clone();
    let mut runtime_config;
    loop {
        let message = match time::timeout(
            Duration::from_secs(RESPONSE_TIMEOUT_SECS),
            next_agent_message(&mut socket),
        )
        .await
        {
            Ok(Ok(message)) => message,
            Ok(Err(error)) => {
                client_log(&format!("websocket receive failed: {error}"));
                report_ready_error(&mut ready_tx, &error);
                return Err(error);
            }
            Err(_) => {
                let error = anyhow!("等待服务端响应超时（{} 秒）", RESPONSE_TIMEOUT_SECS);
                client_log(&format!("websocket receive timeout: {error}"));
                report_ready_error(&mut ready_tx, &error);
                return Err(error);
            }
        };
        client_log(&format!(
            "receive event={} code={}",
            message.event, message.code
        ));
        if message.code != 0 {
            let error = anyhow!(if message.error_msg.is_empty() {
                "服务端拒绝登录".to_owned()
            } else {
                message.error_msg
            });
            client_log(&format!("server rejected login: {error}"));
            report_ready_error(&mut ready_tx, &error);
            return Err(error);
        }
        match message.event.as_str() {
            "auth.ok" => {
                emit_login_status(&app, LoginStage::FetchingConfig);
                if let Ok(mut session_state) = session_state.lock() {
                    session_state.authenticated = true;
                }
                if let Some(token) = message.data.get("deviceToken").and_then(Value::as_str) {
                    state.device_token = token.to_owned();
                    state.username = username.clone();
                    if let Err(error) = save_state(&state_path, &state) {
                        report_ready_error(&mut ready_tx, &error);
                        return Err(error);
                    }
                }
                if let Some(name) = message.data.get("username").and_then(Value::as_str) {
                    username_from_server = name.to_owned();
                }
            }
            "config.snapshot" => {
                runtime_config = match parse_config(message.data) {
                    Ok(config) => config,
                    Err(error) => {
                        report_ready_error(&mut ready_tx, &error);
                        return Err(error);
                    }
                };
                if let Ok(mut session_state) = session_state.lock() {
                    session_state.runtime_config = Some(runtime_config.clone());
                    session_state.config_loaded = true;
                }
                break;
            }
            _ => {}
        }
    }

    emit_login_status(&app, LoginStage::StartingCore);
    let mut core = None;
    if *vpn_rx.borrow() {
        core = Some(
            match CoreProcess::start(&app, &runtime_config.config, &state.device_id) {
                Ok(core) => core,
                Err(error) => {
                    report_ready_error(&mut ready_tx, &error);
                    return Err(error);
                }
            },
        );
        if let Ok(mut session_state) = session_state.lock() {
            session_state.rpc_port = core.as_ref().map(|value| value.rpc_port);
            session_state.core_started = true;
            session_state.vpn_enabled = true;
        }
        client_log("easytier-core started");
    }
    emit_login_status(&app, LoginStage::Ready);
    let _ = ready_tx
        .take()
        .expect("ready sender already consumed")
        .send(Ok(LoginResult {
            username: username_from_server,
            network_name: runtime_config.network_name.clone(),
        }));
    let mut interval = time::interval(Duration::from_secs(15));
    interval.set_missed_tick_behavior(MissedTickBehavior::Delay);
    loop {
        tokio::select! {
            _ = &mut stop_rx => {
                if let Some(mut core) = core {
                    core.stop();
                }
                return Ok(());
            }
            changed = vpn_rx.changed() => {
                changed.map_err(|_| anyhow!("VPN 控制通道已关闭"))?;
                let enabled = *vpn_rx.borrow();
                if enabled && core.is_none() {
                    core = Some(CoreProcess::start(&app, &runtime_config.config, &state.device_id)?);
                    if let Ok(mut state) = session_state.lock() {
                        state.rpc_port = core.as_ref().map(|value| value.rpc_port);
                        state.core_started = true;
                        state.vpn_enabled = true;
                        state.last_error = None;
                    }
                    client_log("easytier-core started by user");
                } else if !enabled {
                    if let Some(mut running) = core.take() {
                        running.stop();
                    }
                    if let Ok(mut state) = session_state.lock() {
                        state.rpc_port = None;
                        state.core_started = false;
                        state.vpn_enabled = false;
                    }
                    client_log("easytier-core stopped by user");
                }
            }
            message = socket.next() => {
                match message {
                    Some(Ok(Message::Text(text))) => {
                        let message: AgentMessage = serde_json::from_str(&text)?;
                        if message.event == "session.revoked" {
                            if let Some(mut core) = core {
                                core.stop();
                            }
                            return Err(anyhow!("设备已被管理员撤销"));
                        }
                    }
                    Some(Ok(Message::Close(_))) | None => {
                        if let Some(mut core) = core {
                            core.stop();
                        }
                        return Err(anyhow!("连接已断开"));
                    }
                    Some(Err(error)) => {
                        if let Some(mut core) = core {
                            core.stop();
                        }
                        return Err(error.into());
                    }
                    _ => {}
                }
            }
            _ = interval.tick() => {
                socket.send(Message::Text(json!({"event":"heartbeat","data":{}}).to_string().into())).await?;
                socket.send(Message::Text(json!({"event":"config.request","data":{}}).to_string().into())).await?;
                if let Ok(message) = next_agent_message(&mut socket).await {
                    if message.event == "config.snapshot" {
                        let config = parse_config(message.data)?;
                        if config.revision != runtime_config.revision {
                            if let Some(mut running) = core.take() {
                                running.stop();
                            }
                            if *vpn_rx.borrow() {
                                core = Some(CoreProcess::start(&app, &config.config, &state.device_id)?);
                                if let Ok(mut session_state) = session_state.lock() {
                                    session_state.rpc_port = core.as_ref().map(|value| value.rpc_port);
                                    session_state.core_started = true;
                                }
                            }
                            if let Ok(mut session_state) = session_state.lock() {
                                session_state.runtime_config = Some(config.clone());
                            }
                            runtime_config = config;
                        }
                    }
                }
            }
        }
    }
}

fn emit_login_status(app: &tauri::AppHandle, stage: LoginStage) {
    let _ = app.emit(
        "login-status",
        serde_json::json!({
            "step": stage.step(),
            "message": stage.message(),
        }),
    );
}

fn report_ready_error(
    ready_tx: &mut Option<tokio::sync::oneshot::Sender<Result<LoginResult>>>,
    error: &anyhow::Error,
) {
    client_log(&format!("login failed: {error}"));
    if let Some(sender) = ready_tx.take() {
        let _ = sender.send(Err(anyhow!(error.to_string())));
    }
}

fn client_log(message: &str) {
    let timestamp = SystemTime::now()
        .duration_since(SystemTime::UNIX_EPOCH)
        .map(|value| value.as_secs())
        .unwrap_or_default();
    let line = format!("[{timestamp}] {message}\n");
    eprint!("[client] {line}");
    if let Ok(state_path) = state_path() {
        let log_path = state_path.with_file_name("client.log");
        if let Ok(mut file) = fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(log_path)
        {
            let _ = file.write_all(line.as_bytes());
        }
    }
}

fn read_log_tail(path: &Path) -> String {
    const MAX_LOG_BYTES: usize = 128 * 1024;
    let Ok(bytes) = fs::read(path) else {
        return String::new();
    };
    let start = bytes.len().saturating_sub(MAX_LOG_BYTES);
    String::from_utf8_lossy(&bytes[start..]).into_owned()
}

async fn next_agent_message<S>(socket: &mut S) -> Result<AgentMessage>
where
    S: futures_util::Stream<Item = Result<Message, tokio_tungstenite::tungstenite::Error>> + Unpin,
{
    while let Some(message) = socket.next().await {
        match message? {
            Message::Text(text) => return Ok(serde_json::from_str(&text)?),
            Message::Ping(_) | Message::Pong(_) => continue,
            Message::Close(_) => return Err(anyhow!("连接已关闭")),
            _ => continue,
        }
    }
    Err(anyhow!("连接已断开"))
}

fn parse_config(data: Value) -> Result<RuntimeConfig> {
    serde_json::from_value(data).context("解析服务端配置失败")
}

fn agent_url(server: &str) -> Result<Url> {
    let mut url = Url::parse(server.trim_end_matches('/'))?;
    url.set_scheme(if url.scheme() == "https" { "wss" } else { "ws" })
        .map_err(|_| anyhow!("服务端地址协议不支持"))?;
    url.set_path("/agent/socket/");
    Ok(url)
}

fn normalize_server_url(server: &str) -> Result<String> {
    let value = server.trim().trim_end_matches('/').to_owned();
    let url = Url::parse(&value).context("服务地址格式不正确")?;
    if !matches!(url.scheme(), "http" | "https") || url.host_str().is_none() {
        return Err(anyhow!("服务地址必须使用 http 或 https，并包含主机名"));
    }
    Ok(value)
}

fn state_path() -> Result<PathBuf> {
    let dirs =
        ProjectDirs::from("com", "enterprise", "EnterpriseVPN").context("无法获取应用配置目录")?;
    fs::create_dir_all(dirs.config_dir())?;
    Ok(dirs.config_dir().join("device.json"))
}

fn load_state(path: &Path) -> Result<DeviceState> {
    if path.exists() {
        let mut state: DeviceState = serde_json::from_slice(&fs::read(path)?)?;
        if !state.device_id.is_empty() {
            if state.server_url.trim().is_empty() {
                state.server_url = default_server_url();
                save_state(path, &state)?;
            }
            return Ok(state);
        }
    }
    let state = DeviceState {
        device_id: Uuid::new_v4().to_string(),
        device_token: String::new(),
        username: String::new(),
        server_url: default_server_url(),
    };
    save_state(path, &state)?;
    Ok(state)
}

fn save_state(path: &Path, state: &DeviceState) -> Result<()> {
    fs::write(path, serde_json::to_vec_pretty(state)?)?;
    Ok(())
}

struct CoreProcess {
    child: Child,
    config_path: PathBuf,
    rpc_port: u16,
}

impl CoreProcess {
    fn start(app: &tauri::AppHandle, config: &str, device_id: &str) -> Result<Self> {
        if config.trim().is_empty() {
            return Err(anyhow!("服务端未下发 EasyTier 配置"));
        }
        let path = std::env::temp_dir().join(format!("enterprise-vpn-{device_id}.toml"));
        fs::write(&path, config)?;
        let executable = bundled_core_path(app)?;
        let rpc_port = free_rpc_port()?;
        let log_path = state_path()?.with_file_name("easytier-core.log");
        let stdout = fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(&log_path)?;
        let stderr = stdout.try_clone()?;
        let mut command = Command::new(executable);
        #[cfg(target_os = "windows")]
        {
            use std::os::windows::process::CommandExt;

            command.creation_flags(0x08000000);
        }
        let child = command
            .args(["--config-file", path.to_string_lossy().as_ref()])
            .arg("--rpc-portal")
            .arg(format!("127.0.0.1:{rpc_port}"))
            .stdout(std::process::Stdio::from(stdout))
            .stderr(std::process::Stdio::from(stderr))
            .spawn()
            .context("启动 easytier-core 失败")?;
        Ok(Self {
            child,
            config_path: path,
            rpc_port,
        })
    }

    fn stop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
        let _ = fs::remove_file(&self.config_path);
    }
}

fn free_rpc_port() -> Result<u16> {
    Ok(TcpListener::bind(("127.0.0.1", 0))?.local_addr()?.port())
}

#[derive(Debug, Deserialize)]
struct CliPeerRecord {
    #[serde(rename = "ipv4", default)]
    cidr: String,
    #[serde(default)]
    hostname: String,
    #[serde(default)]
    cost: String,
    #[serde(default)]
    lat_ms: String,
    #[serde(default)]
    loss_rate: String,
    #[serde(default)]
    rx_bytes: String,
    #[serde(default)]
    tx_bytes: String,
    #[serde(default)]
    tunnel_proto: String,
    #[serde(default)]
    nat_type: String,
    #[serde(default)]
    id: String,
    #[serde(default)]
    version: String,
}

async fn query_easytier_status(app: &tauri::AppHandle, rpc_port: u16) -> Result<EasyTierStatus> {
    let cli_path = bundled_cli_path(app)?;
    let peer_output = run_easytier_cli(&cli_path, rpc_port, &["peer", "list"])?;
    let records: Vec<CliPeerRecord> =
        serde_json::from_slice(&peer_output).context("解析 EasyTier 对等节点状态失败")?;
    let local = records
        .iter()
        .find(|peer| peer.cost.eq_ignore_ascii_case("local"))
        .context("EasyTier 尚未返回本机节点")?;
    let node_output = run_easytier_cli(&cli_path, rpc_port, &["node", "info"]);
    let node = node_output
        .ok()
        .and_then(|output| serde_json::from_slice::<Value>(&output).ok())
        .map(|node| {
            let virtual_ipv4 = json_string_field(&node, "ipv4_addr", "ipv4Addr");
            let version = json_string_field(&node, "version", "version");
            (node, virtual_ipv4, version)
        });
    // With DHCP enabled, the address is assigned asynchronously and is exposed
    // by `node info`; the local row in `peer list` can still be empty.
    let virtual_ipv4 = node
        .as_ref()
        .map(|(_, virtual_ipv4, _)| virtual_ipv4.clone())
        .filter(|value| !value.is_empty())
        .unwrap_or_else(|| local.cidr.clone());
    let version = node
        .as_ref()
        .map(|(_, _, version)| version.clone())
        .filter(|value| !value.is_empty())
        .unwrap_or_else(|| local.version.clone());
    let nat_type = local.nat_type.clone();
    let port_range = node
        .as_ref()
        .map(|(node, _, _)| rpc_port_range(node))
        .unwrap_or_else(|| "未知".to_owned());

    let rx_bytes = records.iter().map(|peer| parse_size(&peer.rx_bytes)).sum();
    let tx_bytes = records.iter().map(|peer| parse_size(&peer.tx_bytes)).sum();
    let peers = records
        .into_iter()
        .map(|peer| {
            let local = peer.cost.eq_ignore_ascii_case("local");
            EasyTierPeer {
                id: peer.id,
                hostname: peer.hostname,
                virtual_ipv4: peer.cidr,
                protocol: if local {
                    "本机".to_owned()
                } else {
                    peer.tunnel_proto
                },
                latency_ms: if local {
                    None
                } else {
                    peer.lat_ms.parse().ok()
                },
                loss_rate: if local {
                    None
                } else {
                    peer.loss_rate
                        .trim_end_matches('%')
                        .parse::<f32>()
                        .ok()
                        .map(|value| value / 100.0)
                },
                local,
            }
        })
        .collect();

    Ok(EasyTierStatus {
        running: true,
        vpn_enabled: true,
        version,
        virtual_ipv4,
        nat_type,
        port_range,
        rx_bytes,
        tx_bytes,
        peers,
        server_connected: false,
        authenticated: false,
        config_loaded: false,
        core_started: true,
        rpc_available: true,
        network_name: String::new(),
        revision: String::new(),
        last_error: None,
    })
}

fn run_easytier_cli(cli_path: &Path, rpc_port: u16, command: &[&str]) -> Result<Vec<u8>> {
    let rpc_endpoint = format!("127.0.0.1:{rpc_port}");
    let output = Command::new(cli_path)
        .args(["--rpc-portal", rpc_endpoint.as_str(), "--output", "json"])
        .args(command)
        .output()
        .with_context(|| format!("启动 EasyTier CLI 失败: {}", cli_path.display()))?;
    if output.status.success() {
        return Ok(output.stdout);
    }
    let message = String::from_utf8_lossy(&output.stderr).trim().to_owned();
    Err(anyhow!(if message.is_empty() {
        "EasyTier CLI 查询失败".to_owned()
    } else {
        message
    }))
}

fn rpc_port_range(node: &Value) -> String {
    let stun = node.get("stun_info").or_else(|| node.get("stunInfo"));
    let min_port = stun
        .and_then(|value| value.get("min_port").or_else(|| value.get("minPort")))
        .and_then(Value::as_u64);
    let max_port = stun
        .and_then(|value| value.get("max_port").or_else(|| value.get("maxPort")))
        .and_then(Value::as_u64);
    match (min_port, max_port) {
        (Some(0), Some(0)) | (None, None) => "未知".to_owned(),
        (Some(min), Some(max)) => format!("{min}-{max}"),
        _ => "未知".to_owned(),
    }
}

fn json_string_field(value: &Value, snake_case: &str, camel_case: &str) -> String {
    value
        .get(snake_case)
        .or_else(|| value.get(camel_case))
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

fn parse_size(value: &str) -> u64 {
    let value = value.trim().to_ascii_lowercase();
    let number: String = value
        .chars()
        .take_while(|character| character.is_ascii_digit() || *character == '.')
        .collect();
    let multiplier = if value.contains("tb") {
        1_000_000_000_000.0
    } else if value.contains("gb") {
        1_000_000_000.0
    } else if value.contains("mb") {
        1_000_000.0
    } else if value.contains("kb") {
        1_000.0
    } else {
        1.0
    };
    number
        .parse::<f64>()
        .map(|number| (number * multiplier) as u64)
        .unwrap_or_default()
}

impl Drop for CoreProcess {
    fn drop(&mut self) {
        self.stop();
    }
}

fn bundled_core_path(app: &tauri::AppHandle) -> Result<PathBuf> {
    let name = if cfg!(target_os = "windows") {
        "easytier-core.exe"
    } else {
        "easytier-core"
    };
    bundled_binary_path(app, name)
}

fn bundled_cli_path(app: &tauri::AppHandle) -> Result<PathBuf> {
    let name = if cfg!(target_os = "windows") {
        "easytier-cli.exe"
    } else {
        "easytier-cli"
    };
    bundled_binary_path(app, name)
}

fn bundled_binary_path(app: &tauri::AppHandle, name: &str) -> Result<PathBuf> {
    let resource_dir = app.path().resource_dir()?;
    for path in [
        resource_dir.join("binaries").join(name),
        resource_dir.join(name),
    ] {
        if path.is_file() {
            return Ok(path);
        }
    }

    let executable = std::env::current_exe()?;
    let executable_dir = executable.parent().context("无法定位客户端目录")?;
    for path in [
        executable_dir.join("binaries").join(name),
        executable_dir.join(name),
    ] {
        if path.is_file() {
            return Ok(path);
        }
    }

    Err(anyhow!("未找到 EasyTier 可执行文件: {}", name))
}

#[cfg(test)]
mod tests {
    use super::{
        CliPeerRecord, json_string_field, normalize_server_url, parse_size, rpc_port_range,
    };
    use serde_json::json;

    #[test]
    fn parse_size_supports_cli_decimal_units() {
        assert_eq!(parse_size("512 B"), 512);
        assert_eq!(parse_size("1.5 kB"), 1_500);
        assert_eq!(parse_size("2 MB"), 2_000_000);
    }

    #[test]
    fn rpc_port_range_reads_snake_or_camel_case_fields() {
        assert_eq!(
            rpc_port_range(&json!({ "stun_info": { "min_port": 40000, "max_port": 40020 } })),
            "40000-40020"
        );
        assert_eq!(
            rpc_port_range(&json!({ "stunInfo": { "minPort": 0, "maxPort": 0 } })),
            "未知"
        );
    }

    #[test]
    fn peer_list_reads_easytiers_ipv4_field() {
        let peer: CliPeerRecord = serde_json::from_value(json!({
            "ipv4": "10.10.10.3/24",
            "cost": "Local",
            "hostname": "client"
        }))
        .expect("peer record should deserialize");
        assert_eq!(peer.cidr, "10.10.10.3/24");
    }

    #[test]
    fn node_info_reads_dhcp_assigned_ipv4_address() {
        let node = json!({
            "ipv4_addr": "10.10.10.8/24",
            "version": "2.6.4"
        });
        assert_eq!(
            json_string_field(&node, "ipv4_addr", "ipv4Addr"),
            "10.10.10.8/24"
        );
        assert_eq!(json_string_field(&node, "version", "version"), "2.6.4");
    }

    #[test]
    fn service_url_accepts_http_and_https_only() {
        assert_eq!(
            normalize_server_url(" https://vpn.example.com/ ").unwrap(),
            "https://vpn.example.com"
        );
        assert!(normalize_server_url("ftp://vpn.example.com").is_err());
        assert!(normalize_server_url("vpn.example.com").is_err());
    }
}
