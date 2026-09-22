const tauri = window.__TAURI__
const invoke = tauri.core.invoke
const listen = tauri.event.listen

const form = document.querySelector('#login-form')
const loginView = document.querySelector('#login-view')
const appShell = document.querySelector('.app-shell')
const loginButton = document.querySelector('#login-button')
const username = document.querySelector('#username')
const password = document.querySelector('#password')
const message = document.querySelector('#message')
const connected = document.querySelector('#connected')
const account = document.querySelector('#account')
const network = document.querySelector('#network')
const logout = document.querySelector('#menu-logout')
const vpnToggle = document.querySelector('#vpn-toggle')
const vpnToggleIcon = document.querySelector('#vpn-toggle-icon')
const accountMenuButton = document.querySelector('#account-menu-button')
const accountMenu = document.querySelector('#account-menu')
const menuAccount = document.querySelector('#menu-account')
const coreState = document.querySelector('#core-state')
const coreVersion = document.querySelector('#core-version')
const virtualIp = document.querySelector('#virtual-ip')
const natType = document.querySelector('#nat-type')
const portRange = document.querySelector('#port-range')
const peers = document.querySelector('#peers')
const peerCount = document.querySelector('#peer-count')
const downloadRate = document.querySelector('#download-rate')
const uploadRate = document.querySelector('#upload-rate')
const connectionSummary = document.querySelector('#connection-summary')
const configState = document.querySelector('#config-state')
const configNetwork = document.querySelector('#config-network')
const configRevision = document.querySelector('#config-revision')
const configContent = document.querySelector('#config-content')
const clientLogs = document.querySelector('#client-logs')
const easytierLogs = document.querySelector('#easytier-logs')
const refreshLogs = document.querySelector('#refresh-logs')
const tabButtons = [...document.querySelectorAll('.tab-button')]
const tabPanels = [...document.querySelectorAll('.tab-panel')]
const loading = document.querySelector('#loading')
const loadingMessage = document.querySelector('#loading-message')
const loadingSteps = [...document.querySelectorAll('.loading-steps span')]

let loadingActive = false
let statusTimer = null
let previousTraffic = null
let previousTrafficAt = 0
let vpnEnabled = true

function showLoading(active) {
  loadingActive = active
  loading.hidden = !active
  loginButton.disabled = active
  username.disabled = active
  password.disabled = active
}

function updateLoading(payload) {
  const step = typeof payload === 'object' ? payload.step : 0
  const text = typeof payload === 'object' ? payload.message : payload
  loadingMessage.textContent = text || '正在建立安全连接…'
  loadingSteps.forEach((item, index) => {
    item.classList.toggle('active', index <= step)
  })
}

function errorMessage(error) {
  if (typeof error === 'string') return error
  if (error && typeof error.message === 'string') return error.message
  return '登录失败，请检查账号或稍后重试'
}

function wait(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

function formatRate(bytesPerSecond) {
  if (!Number.isFinite(bytesPerSecond) || bytesPerSecond <= 0) return '0'
  return bytesPerSecond >= 1024 ? (bytesPerSecond / 1024).toFixed(1) : String(Math.round(bytesPerSecond))
}

function formatLatency(value) {
  return value == null ? '-' : `${Number(value).toFixed(1)} ms`
}

function formatLoss(value) {
  return value == null ? '-' : `丢包 ${(Number(value) * 100).toFixed(1)}%`
}

function renderPeers(items) {
  peerCount.textContent = `${items.length} 个节点`
  if (!items.length) {
    peers.innerHTML = '<p class="empty-state">暂未发现对等节点</p>'
    return
  }
  peers.innerHTML = items.map((peer) => `
    <article class="peer-item ${peer.local ? 'peer-local' : ''}">
      <div class="peer-icon" aria-hidden="true">${peer.local ? '⌂' : '▣'}</div>
      <div class="peer-main">
        <strong>${escapeHtml(peer.hostname || '未命名节点')}</strong>
        <span>ID: ${escapeHtml(peer.id)}</span>
        <span>IP: ${escapeHtml(peer.virtual_ipv4 || '-')}</span>
      </div>
      <div class="peer-meta">
        ${peer.local ? '<b>本地</b>' : `<strong>${formatLatency(peer.latency_ms)}</strong><span>${escapeHtml(peer.protocol || '-')}</span><small>${formatLoss(peer.loss_rate)}</small>`}
      </div>
    </article>
  `).join('')
}

function escapeHtml(value) {
  return String(value).replace(/[&<>'"]/g, (character) => ({
    '&': '&amp;',
    '<': '&lt;',
    '>': '&gt;',
    "'": '&#39;',
    '"': '&quot;',
  }[character]))
}

function renderStatus(data) {
  const running = Boolean(data?.running)
  vpnEnabled = Boolean(data?.vpn_enabled)
  vpnToggle.classList.toggle('vpn-toggle-on', vpnEnabled)
  vpnToggle.classList.toggle('vpn-toggle-off', !vpnEnabled)
  vpnToggle.setAttribute('aria-pressed', String(vpnEnabled))
  vpnToggle.title = vpnEnabled ? '关闭 EasyTier VPN' : '启动 EasyTier VPN'
  vpnToggle.setAttribute('aria-label', vpnEnabled ? '关闭 EasyTier VPN' : '启动 EasyTier VPN')
  vpnToggleIcon.textContent = vpnEnabled ? '■' : '▶'
  coreState.className = `state-badge ${running ? 'state-badge-running' : 'state-badge-error'}`
  coreState.innerHTML = `<i></i>${running ? '运行中' : '未连接'}`
  coreVersion.textContent = data?.version ? `EasyTier ${data.version}` : 'EasyTier 核心未运行'
  virtualIp.textContent = data?.virtual_ipv4 || '-'
  natType.textContent = data?.nat_type || '-'
  portRange.textContent = data?.port_range || '-'
  renderPeers(data?.peers || [])
  const chain = {
    server: [data?.server_connected, '已连接', '未连接'],
    auth: [data?.authenticated, '已认证', '未完成'],
    config: [data?.config_loaded, `已拉取${data?.revision ? ` · ${data.revision}` : ''}`, '未拉取'],
    core: [data?.core_started, '已启动', '未启动'],
    rpc: [data?.rpc_available && data?.running, '已连接', '未连接'],
  }
  Object.entries(chain).forEach(([name, [ok, success, failure]]) => {
    const item = document.querySelector(`[data-chain="${name}"]`)
    item.classList.toggle('chain-ok', Boolean(ok))
    item.classList.toggle('chain-failed', !ok)
    item.querySelector('strong').textContent = ok ? success : failure
  })
  const fullyConnected = Boolean(data?.server_connected && data?.authenticated && data?.config_loaded && data?.core_started && data?.rpc_available && data?.running)
  connectionSummary.textContent = fullyConnected ? 'VPN 已连接' : (data?.last_error || '连接未完成')
  connectionSummary.className = fullyConnected ? 'summary-ok' : 'summary-failed'

  const now = Date.now()
  if (previousTraffic && previousTrafficAt) {
    const elapsed = Math.max((now - previousTrafficAt) / 1000, 0.1)
    downloadRate.textContent = formatRate((Number(data.rx_bytes) - previousTraffic.rx) / elapsed)
    uploadRate.textContent = formatRate((Number(data.tx_bytes) - previousTraffic.tx) / elapsed)
  } else {
    downloadRate.textContent = '0'
    uploadRate.textContent = '0'
  }
  previousTraffic = { rx: Number(data?.rx_bytes || 0), tx: Number(data?.tx_bytes || 0) }
  previousTrafficAt = now
}

async function refreshStatus() {
  try {
    renderStatus(await invoke('get_easytier_status'))
  } catch (error) {
    coreState.className = 'state-badge state-badge-error'
    coreState.innerHTML = '<i></i>状态读取失败'
    coreVersion.textContent = errorMessage(error)
    peers.innerHTML = '<p class="empty-state">暂时无法读取对等节点信息</p>'
  }
}

function startStatusPolling() {
  stopStatusPolling()
  previousTraffic = null
  previousTrafficAt = 0
  refreshStatus()
  statusTimer = window.setInterval(refreshStatus, 3000)
}

function stopStatusPolling() {
  if (statusTimer) window.clearInterval(statusTimer)
  statusTimer = null
}

async function refreshConfig() {
  try {
    const data = await invoke('get_client_config')
    configState.textContent = data.loaded ? '已从服务端拉取' : '未拉取'
    configState.className = data.loaded ? 'summary-ok' : 'summary-failed'
    configNetwork.textContent = data.network_name || '-'
    configRevision.textContent = data.revision || '-'
    configContent.textContent = data.config || '服务端尚未下发配置'
  } catch (error) {
    configState.textContent = '读取失败'
    configContent.textContent = errorMessage(error)
  }
}

async function refreshLogsView() {
  try {
    const data = await invoke('get_client_logs')
    clientLogs.textContent = data.client || '暂无客户端日志'
    easytierLogs.textContent = data.easytier || '暂无 EasyTier 核心日志'
    clientLogs.scrollTop = clientLogs.scrollHeight
    easytierLogs.scrollTop = easytierLogs.scrollHeight
  } catch (error) {
    clientLogs.textContent = errorMessage(error)
    easytierLogs.textContent = errorMessage(error)
  }
}

function activateTab(name) {
  tabButtons.forEach((button) => button.classList.toggle('active', button.dataset.tab === name))
  tabPanels.forEach((panel) => panel.classList.toggle('active', panel.dataset.panel === name))
  if (name === 'config') refreshConfig()
  if (name === 'logs') refreshLogsView()
}

function setAccountMenu(open) {
  accountMenu.hidden = !open
  accountMenuButton.setAttribute('aria-expanded', String(open))
}

async function setVpnEnabled(enabled) {
  vpnToggle.disabled = true
  try {
    await invoke('set_vpn_enabled', { enabled })
    await wait(250)
    await refreshStatus()
  } catch (error) {
    message.textContent = errorMessage(error)
  } finally {
    vpnToggle.disabled = false
  }
}

async function setWindowMode(connectedMode) {
  try {
    await invoke('set_window_mode', { connected: connectedMode })
  } catch (error) {
    message.textContent = errorMessage(error)
  }
}

listen('login-status', (event) => updateLoading(event.payload))
tabButtons.forEach((button) => button.addEventListener('click', () => activateTab(button.dataset.tab)))
refreshLogs.addEventListener('click', refreshLogsView)
vpnToggle.addEventListener('click', () => setVpnEnabled(!vpnEnabled))
accountMenuButton.addEventListener('click', () => setAccountMenu(accountMenu.hidden))
document.addEventListener('click', (event) => {
  if (!event.target.closest('.account-menu-wrap')) setAccountMenu(false)
})
document.addEventListener('keydown', (event) => {
  if (event.key === 'Escape') setAccountMenu(false)
})

async function enterConnected(result) {
  account.textContent = result.username
  menuAccount.textContent = result.username
  network.textContent = result.network_name || '企业内网'
  form.hidden = true
  loginView.hidden = true
  connected.hidden = false
  appShell.classList.add('connected-mode')
  showLoading(false)
  await setWindowMode(true)
  startStatusPolling()
  refreshConfig()
}

form.addEventListener('submit', async (event) => {
  event.preventDefault()
  if (loadingActive) return

  message.textContent = ''
  showLoading(true)
  updateLoading({ step: 0, message: '正在连接服务端…' })

  try {
    const result = await invoke('login', {
      username: username.value.trim(),
      password: password.value,
    })
    updateLoading({ step: 4, message: '连接成功，欢迎回来' })
    await wait(550)
    await enterConnected(result)
  } catch (error) {
    showLoading(false)
    message.textContent = errorMessage(error)
  }
})

logout.addEventListener('click', async () => {
  logout.disabled = true
  try {
    await invoke('logout')
    setAccountMenu(false)
    stopStatusPolling()
    form.hidden = false
    loginView.hidden = false
    connected.hidden = true
    appShell.classList.remove('connected-mode')
    activateTab('status')
    password.value = ''
    message.textContent = '已退出登录'
    await setWindowMode(false)
    username.focus()
  } catch (error) {
    message.textContent = errorMessage(error)
  } finally {
    logout.disabled = false
  }
})

window.addEventListener('DOMContentLoaded', async () => {
  try {
    updateLoading({ step: 0, message: '正在恢复登录状态…' })
    showLoading(true)
    const result = await invoke('restore_session')
    await enterConnected(result)
  } catch {
    showLoading(false)
  }
})
