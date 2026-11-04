<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  api,
  APIRequestError,
  type DashboardData,
  type DiscoveredContainer,
  type DoctorCheck,
  type RouteRecord,
  type StepProgress,
  type UpdateInfo,
} from './api'
import AuthGate from './components/AuthGate.vue'
import PublishModal from './components/PublishModal.vue'
import InspectorDrawer from './components/InspectorDrawer.vue'
import NodeModal from './components/NodeModal.vue'
import DNSModal from './components/DNSModal.vue'
import CommandPalette from './components/CommandPalette.vue'

interface PathHop {
  key: string
  title: string
  val: string
  status: 'ok' | 'warn' | 'err' | 'info'
}

const bootLoading = ref(true)
const initialized = ref(false)
const authenticated = ref(false)
const version = ref('v0.1.0')
const theme = ref<'light' | 'dark'>('light')

const activeView = ref<'dashboard' | 'nodes' | 'dns' | 'cli'>('dashboard')
const layoutMode = ref<'cards' | 'table'>('cards')
const searchQuery = ref('')
const nodeFilter = ref('all')
const statusFilter = ref('all')

const dash = ref<DashboardData | null>(null)
const doctorChecks = ref<DoctorCheck[]>([])
const doctorBusy = ref(false)

const showPublishModal = ref(false)
const publishPresetContainer = ref<DiscoveredContainer | null>(null)
const livePublishSteps = ref<StepProgress[]>([])

const showNodeModal = ref(false)
const showDNSModal = ref(false)
const showCmdPalette = ref(false)
const inspectedRouteId = ref<string | null>(null)
const expandAllUnexposed = ref(false)
const proxyBusyMap = ref<Record<string, boolean>>({})

const newTokenName = ref('cli-token')
const newTokenScope = ref<'admin' | 'readonly'>('admin')
const createdPlainToken = ref('')

const curPassword = ref('')
const nextPassword = ref('')

const updateInfo = ref<UpdateInfo | null>(null)
const updateChecking = ref(false)
const updateApplying = ref(false)

const toastText = ref('')
let toastTimer: ReturnType<typeof setTimeout> | null = null
let sseSource: EventSource | null = null

function showToast(msg: string) {
  toastText.value = msg
  if (toastTimer) clearTimeout(toastTimer)
  toastTimer = setTimeout(() => {
    toastText.value = ''
  }, 3200)
}

function applyTheme(next: 'light' | 'dark') {
  theme.value = next
  document.documentElement.setAttribute('data-theme', next)
  localStorage.setItem('vantu_theme', next)
}

function toggleTheme() {
  applyTheme(theme.value === 'light' ? 'dark' : 'light')
}

async function checkAuthAndLoad() {
  bootLoading.value = true
  try {
    const st = await api.getAuthStatus()
    initialized.value = st.initialized
    authenticated.value = st.authenticated
    version.value = st.version || 'v0.1.0'
    if (st.authenticated) {
      await refreshDashboard()
      connectSSE()
      loadUpdateInfo()
    }
  } catch (err) {
    showToast((err as Error).message)
  } finally {
    bootLoading.value = false
  }
}

async function refreshDashboard() {
  try {
    dash.value = await api.getDashboard()
  } catch (err) {
    if (err instanceof APIRequestError && err.code === 'AUTH_REQUIRED') {
      authenticated.value = false
      return
    }
    showToast((err as Error).message)
  }
}

function connectSSE() {
  if (sseSource) sseSource.close()
  const es = new EventSource('/api/v1/events/stream')
  es.addEventListener('route_step', (ev: MessageEvent) => {
    try {
      const parsed = JSON.parse(ev.data)
      const d = parsed.data as StepProgress
      const idx = livePublishSteps.value.findIndex((s) => s.step === d.step)
      if (idx >= 0) {
        livePublishSteps.value[idx] = d
      } else {
        livePublishSteps.value.push(d)
      }
    } catch {
      // Ignore malformed frame
    }
  })
  es.addEventListener('update_available', (ev: MessageEvent) => {
    try {
      updateInfo.value = JSON.parse(ev.data).data as UpdateInfo
    } catch {
      // Ignore malformed frame
    }
  })
  const reloadEvents = ['routes_updated', 'nodes_updated', 'containers_updated', 'probe_completed']
  for (const name of reloadEvents) {
    es.addEventListener(name, () => {
      refreshDashboard()
    })
  }
  sseSource = es
}

function handleKeydown(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') {
    e.preventDefault()
    if (authenticated.value) {
      showCmdPalette.value = !showCmdPalette.value
    }
  } else if (e.key === 'Escape') {
    showCmdPalette.value = false
  }
}

onMounted(() => {
  const saved = localStorage.getItem('vantu_theme')
  if (saved === 'dark' || saved === 'light') {
    applyTheme(saved)
  }
  window.addEventListener('keydown', handleKeydown)
  checkAuthAndLoad()
})

onBeforeUnmount(() => {
  window.removeEventListener('keydown', handleKeydown)
  if (sseSource) sseSource.close()
})

const unexposedList = computed(() =>
  (dash.value?.containers || []).filter((c) => c.published_domains.length === 0)
)

const visibleUnexposedList = computed(() =>
  expandAllUnexposed.value ? unexposedList.value : unexposedList.value.slice(0, 3)
)

const filteredRoutes = computed(() => {
  const all = dash.value?.routes || []
  const q = searchQuery.value.trim().toLowerCase()
  return all.filter((r) => {
    if (nodeFilter.value !== 'all' && r.edge_node_id !== nodeFilter.value) return false
    if (statusFilter.value === 'healthy' && r.lifecycle_state !== 'active') return false
    if (statusFilter.value === 'attention' && r.lifecycle_state === 'active') return false
    if (!q) return true
    return (
      r.full_domain.toLowerCase().includes(q) ||
      r.container_name.toLowerCase().includes(q) ||
      r.local_target_host.toLowerCase().includes(q) ||
      String(r.local_target_port).includes(q) ||
      r.edge_node_name.toLowerCase().includes(q)
    )
  })
})

/**
 * Dynamically builds the actual network hops traversed by a route (3, 4, or 5 hops):
 * - Direct + DNS Only: Local -> Edge Node -> Public HTTPS (3 hops)
 * - Relay + DNS Only:  Local -> Local Relay -> Edge Node -> Public HTTPS (4 hops)
 * - Direct + CF CDN:   Local -> Edge Origin -> Cloudflare CDN -> Public HTTPS (4 hops)
 * - Relay + CF CDN:    Local -> Local Relay -> Edge Origin -> Cloudflare CDN -> Public HTTPS (5 hops)
 */
function buildRouteHops(r: RouteRecord): PathHop[] {
  const hops: PathHop[] = []

  hops.push({
    key: 'local',
    title: r.source_type === 'docker' ? '容器' : r.source_type === 'self' ? '本机控制台' : '本地服务',
    val: `:${r.local_target_port} ${r.local_healthy ? '正常' : '离线'}`,
    status: r.local_healthy ? 'ok' : 'err',
  })

  if (r.forward_mode === 'relay') {
    hops.push({
      key: 'relay',
      title: '本机中继',
      val: 'Local Relay',
      status: 'ok',
    })
  }

  hops.push({
    key: 'edge',
    title: r.dns_proxied ? '边缘源站' : '边缘节点',
    val: `${r.edge_node_name}${r.edge_node_ts_rtt_ms >= 0 ? ` (${r.edge_node_ts_rtt_ms}ms)` : ''}`,
    status: !r.caddy_synced ? 'warn' : r.edge_node_ts_type === 'derp' ? 'warn' : 'ok',
  })

  if (r.dns_proxied) {
    hops.push({
      key: 'cdn',
      title: 'Cloudflare',
      val: r.origin_cert_days >= 0 ? `源站证书 ${r.origin_cert_days}天` : '源站签发中',
      status: !r.origin_tls_ok ? 'info' : r.origin_cert_days < 14 ? 'warn' : 'ok',
    })
  }

  const pubVal = r.dns_proxied
    ? r.public_http_code > 0
      ? `${r.public_http_code} (${r.public_latency_ms}ms)`
      : '探测中'
    : r.public_http_code > 0
    ? `${r.public_http_code}${r.origin_cert_days >= 0 ? ` (${r.origin_cert_days}天)` : ''}`
    : r.origin_cert_days >= 0
    ? `证书 ${r.origin_cert_days}天`
    : '签发中'

  hops.push({
    key: 'public',
    title: '公网 HTTPS',
    val: pubVal,
    status: r.public_http_code >= 500 ? 'err' : !r.origin_tls_ok ? 'info' : 'ok',
  })

  return hops
}

function openPublish(container?: DiscoveredContainer) {
  publishPresetContainer.value = container || null
  livePublishSteps.value = []
  showPublishModal.value = true
}

function dotClass(state: string) {
  switch (state) {
    case 'active':
      return 'healthy'
    case 'issuing_tls':
    case 'provisioning':
      return 'issuing'
    case 'paused':
      return 'degraded'
    default:
      return 'broken'
  }
}

function stateBadge(state: string): { label: string; cls: string } {
  switch (state) {
    case 'active':
      return { label: '正常', cls: 'ok' }
    case 'issuing_tls':
      return { label: '证书签发中', cls: 'info' }
    case 'config_drift':
      return { label: '未同步', cls: 'warn' }
    case 'paused':
      return { label: '已暂停', cls: '' }
    default:
      return { label: '异常', cls: 'err' }
  }
}

async function onToggleProxy(r: RouteRecord) {
  if (!r.supports_proxy || proxyBusyMap.value[r.id]) return
  const prev = r.dns_proxied
  const next = !prev
  proxyBusyMap.value[r.id] = true
  r.dns_proxied = next
  try {
    const updated = await api.toggleProxy(r.id, next)
    r.dns_proxied = updated.dns_proxied
    await refreshDashboard()
    showToast(`${updated.full_domain}: ${updated.dns_proxied ? '已开启 CDN' : '已切回 DNS 直连'}`)
  } catch (err) {
    r.dns_proxied = prev
    showToast((err as Error).message)
  } finally {
    delete proxyBusyMap.value[r.id]
  }
}

async function quickHealDrift(nodeId: string) {
  try {
    const res = await api.reconcileNode(nodeId)
    await refreshDashboard()
    showToast(`已同步 ${res.pushed_routes} 条路由`)
  } catch (err) {
    showToast((err as Error).message)
  }
}

async function triggerGlobalProbe() {
  if (!dash.value) return
  showToast('正在检测状态...')
  try {
    await Promise.all(dash.value.nodes.map((n) => api.pingNode(n.id)))
    await Promise.all(dash.value.routes.map((r) => api.probeRoute(r.id)))
    await refreshDashboard()
    showToast('检测完成')
  } catch (err) {
    showToast((err as Error).message)
  }
}

function switchToView(v: 'dashboard' | 'nodes' | 'dns' | 'cli') {
  activeView.value = v
}

const aiPromptText = computed(() => {
  const origin = typeof window !== 'undefined' ? window.location.origin : 'http://127.0.0.1:8089'
  const zones =
    dash.value?.zones.map((z) => `${z.zone_name}${z.is_default ? '(默认)' : ''}${z.supports_proxy ? '[支持CDN]' : ''}`).join(', ') ||
    '暂未配置'
  const nodes =
    dash.value?.nodes.map((n) => `${n.name}(${n.public_ipv4}${n.is_default ? ',默认' : ''})`).join(', ') ||
    '暂未配置'
  const routes =
    dash.value?.routes.map((r) => `${r.full_domain} -> ${r.local_target_host}:${r.local_target_port}`).join(', ') ||
    '暂无'
  const unpub =
    unexposedList.value.map((c) => `${c.name}(:${c.recommended_port})`).join(', ') ||
    '无'

  return `请帮我操作当前「小凡兔 Vantu」网关控制台（将本地 Docker 容器或内网端口发布为公网 HTTPS 服务）。

【当前控制台实时快照】
- 控制台地址: ${origin}
- 可用根域名: ${zones}
- 边缘节点: ${nodes}
- 已发布路由: ${routes}
- 待发布容器: ${unpub}

【第一步：自动检测连接通道（严禁向用户索要明文 API Token）】
请按以下顺序自动探测执行环境：

1. 场景 A（同机 / SSH 环境，免 Token 优先）：
   先检查本机是否存在 \`xft\` 命令或 \`vantu\` 容器：
   \`xft capabilities --json 2>/dev/null || docker exec vantu xft capabilities --json 2>/dev/null\`
   若返回 \`"ok": true\`，后续所有操作直接调用本机 \`xft\`（通过本地 Unix Socket 通信，无需任何 Token）。

2. 场景 B（跨机器远程环境，Token 走本地凭据或环境变量，不进对话上下文）：
   若不在小凡兔宿主机上，请检查当前环境是否已配置远程凭据：
   - 若本机装有 \`xft\` CLI：检查 \`~/.config/vantu/credentials.json\` 或环境变量 \`$VANTU_TOKEN\`；若未配置，请引导我自己在终端执行一次：
     \`xft login --endpoint ${origin} --token <从控制台设置页创建的API_Token>\`
     （凭据保存在我本机 \`0600\` 权限文件中，你直接调用 \`xft ... --json\` 即可，全程不要让我把 Token 发到聊天框）。
   - 若本机未装 \`xft\` CLI（纯 HTTP API 调用）：先检查 \`[ -n "$VANTU_TOKEN" ]\`。若为空，请引导我自己在本地终端或环境配置中设置：
     \`export VANTU_ENDPOINT="${origin}" && export VANTU_TOKEN="<从控制台设置页创建的API_Token>"\`
     设置完成后你直接引用 \`$VANTU_ENDPOINT\` 与 \`$VANTU_TOKEN\` 环境变量发起请求，切勿让我将明文 Token 粘贴到对话中。

【第二步：常用操作速查】
- CLI 方式（同机或已执行 \`xft login\`）：
  - 查看能力与容器：\`xft capabilities --json\`
  - 预览/发布服务：\`xft publish --target <container:容器名|IP:端口|self> --domain <完整域名> --preset <web|sse|webdav|https_insecure> [--proxied] [--dry-run] --json\`
  - 切换 CDN 代理：\`xft route proxy <完整域名> --on|--off --json\`
  - 拨测/暂停/恢复/删除：\`xft route inspect <完整域名> --probe --json\` | \`xft route pause|resume <完整域名> --json\` | \`xft route rm <完整域名> --yes --json\`
- HTTP API 方式（引用本地 \`$VANTU_TOKEN\` 环境变量）：
  - 查看能力：\`curl -sS -H "Authorization: Bearer $VANTU_TOKEN" "${origin}/api/v1/capabilities"\`
  - 发布路由：\`curl -sS -X POST -H "Authorization: Bearer $VANTU_TOKEN" -H "Content-Type: application/json" "${origin}/api/v1/routes" -d '{"target":"container:容器名","domain":"子域名.根域名","preset":"web","dns_proxied":false}'\`
  - 切换 CDN：\`curl -sS -X PATCH -H "Authorization: Bearer $VANTU_TOKEN" -H "Content-Type: application/json" "${origin}/api/v1/routes/<完整域名>/proxy" -d '{"proxied":true}'\`
  - 删除路由：\`curl -sS -X DELETE -H "Authorization: Bearer $VANTU_TOKEN" "${origin}/api/v1/routes/<完整域名>?delete_dns=true"\`

请先自动检测你当前可用的连接方式，然后协助我完成以下需求：`
})

async function handleCreateToken() {
  try {
    const res = await api.createToken(newTokenName.value, newTokenScope.value)
    createdPlainToken.value = res.plain_text
    await refreshDashboard()
    showToast('已创建 API Token')
  } catch (err) {
    showToast((err as Error).message)
  }
}

async function handleDeleteToken(id: string) {
  try {
    await api.deleteToken(id)
    await refreshDashboard()
    showToast('已删除 Token')
  } catch (err) {
    showToast((err as Error).message)
  }
}

async function runDoctor() {
  doctorBusy.value = true
  try {
    const rep = await api.getDoctor()
    doctorChecks.value = rep.checks
    showToast(rep.healthy ? '自检通过' : '存在异常项')
  } catch (err) {
    showToast((err as Error).message)
  } finally {
    doctorBusy.value = false
  }
}

async function handleChangePassword() {
  try {
    await api.changePassword(curPassword.value, nextPassword.value)
    curPassword.value = ''
    nextPassword.value = ''
    showToast('密码已修改')
  } catch (err) {
    showToast((err as Error).message)
  }
}

async function handleLogout() {
  await api.logout()
  if (sseSource) sseSource.close()
  authenticated.value = false
  initialized.value = true
}

async function loadUpdateInfo(refresh = false) {
  updateChecking.value = true
  try {
    updateInfo.value = await api.getUpdateInfo(refresh)
    if (refresh && updateInfo.value.check_error) {
      showToast(updateInfo.value.check_error)
    } else if (refresh && !updateInfo.value.has_update) {
      showToast('已是最新版本')
    }
  } catch (err) {
    if (refresh) showToast((err as Error).message)
  } finally {
    updateChecking.value = false
  }
}

async function handleApplyUpdate() {
  const target = updateInfo.value?.latest_version
  if (!target || !confirm(`确认更新至 ${target}？更新完成后服务会自动重启，期间控制台短暂不可用。`)) return
  updateApplying.value = true
  try {
    await api.applyUpdate()
    showToast(`已更新至 ${target}，正在等待服务重启...`)
    const deadline = Date.now() + 90_000
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, 2000))
      try {
        const h = await api.getHealth()
        if (h.version === target) {
          window.location.reload()
          return
        }
      } catch {
        // Service is restarting
      }
    }
    showToast('服务重启超时，请检查进程状态后手动刷新页面')
  } catch (err) {
    showToast((err as Error).message)
  } finally {
    updateApplying.value = false
  }
}

function copyText(text: string, label: string) {
  navigator.clipboard?.writeText(text)
  showToast(`已复制${label}`)
}
</script>

<template>
  <div v-if="bootLoading" class="auth-shell">
    <p style="font-size:15px;color:var(--text-secondary);">加载中...</p>
  </div>

  <AuthGate
    v-else-if="!initialized || !authenticated"
    :initialized="initialized"
    :version="version"
    :theme="theme"
    @authenticated="checkAuthAndLoad"
    @toggle-theme="toggleTheme"
  />

  <div v-else>
    <!-- Topbar -->
    <header class="topbar">
      <div class="topbar-inner">
        <div class="brand">
          <div class="brand-mark" aria-hidden="true">
            <svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
              <path d="M7 10V5a2 2 0 0 1 4 0v5"/>
              <path d="M13 10V4a2 2 0 0 1 4 0v6"/>
              <rect x="5" y="10" width="14" height="10" rx="5"/>
              <circle cx="9.5" cy="14.5" r="1"/>
              <circle cx="14.5" cy="14.5" r="1"/>
            </svg>
          </div>
          <span class="brand-title">小凡兔 Vantu</span>
          <span class="brand-tag tnum">{{ version }}</span>
          <button
            v-if="updateInfo?.has_update"
            type="button"
            class="update-pill tnum"
            :title="`发现新版本 ${updateInfo.latest_version}，点击查看`"
            @click="switchToView('cli')"
          >
            新版本 {{ updateInfo.latest_version }}
          </button>
        </div>

        <nav class="nav-tabs" aria-label="主导航">
          <button
            type="button"
            class="nav-tab"
            :class="{ active: activeView === 'dashboard' }"
            @click="switchToView('dashboard')"
          >
            路由 <span class="nav-count tnum">{{ dash?.routes.length || 0 }}</span>
          </button>
          <button
            type="button"
            class="nav-tab"
            :class="{ active: activeView === 'nodes' }"
            @click="switchToView('nodes')"
          >
            节点 <span class="nav-count tnum">{{ dash?.nodes.length || 0 }}</span>
          </button>
          <button
            type="button"
            class="nav-tab"
            :class="{ active: activeView === 'dns' }"
            @click="switchToView('dns')"
          >
            域名 <span class="nav-count tnum">{{ dash?.zones.length || 0 }}</span>
          </button>
          <button
            type="button"
            class="nav-tab"
            :class="{ active: activeView === 'cli' }"
            @click="switchToView('cli')"
          >
            设置
          </button>
        </nav>

        <div class="top-actions">
          <button type="button" class="cmd-trigger" @click="showCmdPalette = true" aria-label="快速搜索">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>
            </svg>
            <span class="cmd-label">搜索</span>
            <span class="kbd">⌘K</span>
          </button>
          <button
            type="button"
            class="icon-btn"
            aria-label="切换明暗主题"
            title="切换明暗主题"
            @click="toggleTheme"
          >
            <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8">
              <circle cx="12" cy="12" r="4"/>
              <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/>
            </svg>
          </button>
          <button type="button" class="btn-primary" @click="openPublish()">
            <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.2">
              <path d="M12 5v14M5 12h14"/>
            </svg>
            发布服务
          </button>
        </div>
      </div>
    </header>

    <main v-if="dash" class="container">
      <!-- VIEW 1: ROUTES -->
      <section v-if="activeView === 'dashboard'">
        <div class="page-head">
          <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap;">
            <h1 class="page-title">服务路由</h1>
            <span v-if="dash.capabilities.tailscale.available" class="badge tnum">
              Tailscale {{ dash.capabilities.tailscale.ipv4 }}
            </span>
          </div>
          <div style="display:flex;gap:8px;flex-wrap:wrap;">
            <button v-if="dash.routes.length > 0" type="button" class="btn-outline" @click="triggerGlobalProbe">
              刷新状态
            </button>
          </div>
        </div>

        <!-- Summary Strip -->
        <div class="summary-strip">
          <div class="summary-cell">
            <div class="summary-label"><span>在线路由</span></div>
            <div class="summary-value tnum">
              {{ dash.summary.active_routes }} / {{ dash.summary.total_routes }}
            </div>
            <div class="summary-foot">
              <span v-if="dash.summary.degraded_count > 0 || dash.summary.drift_count > 0" style="color:var(--err-fg);font-weight:500;">
                {{ dash.summary.degraded_count + dash.summary.drift_count }} 条异常
              </span>
              <span v-else>{{ dash.summary.total_routes > 0 ? '运行正常' : '暂无路由' }}</span>
            </div>
          </div>

          <div class="summary-cell">
            <div class="summary-label"><span>本地未发布容器</span></div>
            <button
              type="button"
              class="summary-value summary-value-btn tnum"
              :disabled="unexposedList.length === 0"
              :title="unexposedList.length > 0 ? '点击选择容器并发布' : ''"
              @click="unexposedList.length > 0 && openPublish(unexposedList[0])"
            >
              {{ unexposedList.length }}
            </button>
            <div class="summary-foot">
              <template v-if="unexposedList.length > 0">
                <button
                  v-for="c in visibleUnexposedList"
                  :key="c.id"
                  type="button"
                  class="discover-chip"
                  @click="openPublish(c)"
                >
                  + {{ c.name }}
                </button>
                <button
                  v-if="unexposedList.length > 3"
                  type="button"
                  class="discover-chip more"
                  :title="expandAllUnexposed ? '收起列表' : `展开其余 ${unexposedList.length - 3} 个未发布容器`"
                  @click="expandAllUnexposed = !expandAllUnexposed"
                >
                  {{ expandAllUnexposed ? '收起' : `... +${unexposedList.length - 3}` }}
                </button>
              </template>
              <span v-else>
                {{ dash.capabilities.docker_available ? '无待发布容器' : '未连接 Docker' }}
              </span>
            </div>
          </div>

          <div class="summary-cell">
            <div class="summary-label"><span>边缘节点</span></div>
            <div class="summary-value tnum">
              {{ dash.summary.nodes_online }} / {{ dash.summary.nodes_total }}
            </div>
            <div class="summary-foot">
              <span v-if="dash.nodes.length > 0" class="tnum">
                默认: {{ dash.nodes[0].name }}
              </span>
              <button v-else type="button" class="discover-chip" @click="showNodeModal = true">
                + 添加节点
              </button>
            </div>
          </div>

          <div class="summary-cell">
            <div class="summary-label"><span>证书最早到期</span></div>
            <div class="summary-value tnum">
              {{ dash.summary.min_cert_days >= 0 ? `${dash.summary.min_cert_days} 天` : '--' }}
            </div>
            <div class="summary-foot">
              <span>{{ dash.summary.min_cert_domain || '暂无证书记录' }}</span>
            </div>
          </div>
        </div>

        <!-- Onboarding / Empty State -->
        <div v-if="dash.routes.length === 0" class="empty-state">
          <h2 style="font-size:18px;font-weight:700;">尚无已发布的服务路由</h2>
          <div style="display:flex;gap:10px;flex-wrap:wrap;justify-content:center;margin-top:6px;">
            <button v-if="dash.zones.length === 0" type="button" class="btn-outline" @click="showDNSModal = true">
              1. 添加 DNS 凭据
            </button>
            <button v-if="dash.nodes.length === 0" type="button" class="btn-outline" @click="showNodeModal = true">
              2. 添加边缘节点
            </button>
            <button type="button" class="btn-primary" @click="openPublish()">
              发布服务
            </button>
          </div>
        </div>

        <template v-else>
          <!-- Toolbar -->
          <div class="toolbar">
            <div class="filter-group">
              <div class="search-box">
                <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                  <circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>
                </svg>
                <input
                  v-model="searchQuery"
                  class="input"
                  type="text"
                  placeholder="搜索域名、容器或端口..."
                  aria-label="搜索路由"
                />
              </div>

              <select v-model="nodeFilter" class="select" aria-label="节点筛选">
                <option value="all">全部节点</option>
                <option v-for="n in dash.nodes" :key="n.id" :value="n.id">{{ n.name }}</option>
              </select>

              <select v-model="statusFilter" class="select" aria-label="状态筛选">
                <option value="all">全部状态</option>
                <option value="healthy">正常</option>
                <option value="attention">异常 / 未同步</option>
              </select>
            </div>

            <div class="segmented" role="group" aria-label="视图切换">
              <button type="button" class="seg-btn" :class="{ active: layoutMode === 'cards' }" @click="layoutMode = 'cards'">
                卡片
              </button>
              <button type="button" class="seg-btn" :class="{ active: layoutMode === 'table' }" @click="layoutMode = 'table'">
                列表
              </button>
            </div>
          </div>

          <!-- Cards Grid (Dynamic 3 / 4 / 5 Hops per Route!) -->
          <div v-if="layoutMode === 'cards'" class="routes-grid">
            <article
              v-for="r in filteredRoutes"
              :key="r.id"
              class="route-card"
              :class="{ broken: r.lifecycle_state === 'degraded' || r.lifecycle_state === 'error' }"
              @click="inspectedRouteId = r.id"
            >
              <div class="route-head">
                <div>
                  <div class="domain-row">
                    <span class="status-dot" :class="dotClass(r.lifecycle_state)"></span>
                    <a
                      class="domain-name"
                      :href="`https://${r.full_domain}`"
                      target="_blank"
                      rel="noreferrer"
                      @click.stop
                    >
                      {{ r.full_domain }}
                    </a>
                    <span class="badge" :class="stateBadge(r.lifecycle_state).cls">
                      {{ stateBadge(r.lifecycle_state).label }}
                    </span>
                  </div>
                  <div class="route-submeta">
                    <span class="badge tnum">
                      {{
                        r.source_type === 'docker'
                          ? r.container_name
                          : r.source_type === 'self'
                          ? `本机控制台 :${r.local_target_port}`
                          : `${r.local_target_host}:${r.local_target_port}`
                      }}
                    </span>
                    <span class="badge">{{ r.edge_node_name }}</span>
                    <span v-if="r.preset !== 'web'" class="badge">{{ r.preset.toUpperCase() }}</span>
                  </div>
                </div>

                <button
                  v-if="r.supports_proxy"
                  type="button"
                  class="cf-switch"
                  :class="{ on: r.dns_proxied, busy: !!proxyBusyMap[r.id] }"
                  :disabled="!!proxyBusyMap[r.id]"
                  :title="proxyBusyMap[r.id] ? '正在切换 CDN 状态...' : r.dns_proxied ? '关闭 Cloudflare CDN' : '开启 Cloudflare CDN'"
                  @click.stop="onToggleProxy(r)"
                >
                  <span>{{ proxyBusyMap[r.id] ? '切换中' : 'CDN' }}</span>
                  <span class="switch-pill"><span class="switch-knob"></span></span>
                </button>
              </div>

              <!-- Dynamic Forwarding Path (only renders actual hops of this route) -->
              <div class="topo-box">
                <div
                  class="topo-track"
                  :style="{ '--hop-count': buildRouteHops(r).length }"
                >
                  <div
                    v-for="(hop, idx) in buildRouteHops(r)"
                    :key="hop.key"
                    class="topo-node"
                  >
                    <div class="topo-line-row">
                      <span
                        class="topo-station"
                        :class="{
                          err: hop.status === 'err',
                          warn: hop.status === 'warn',
                          info: hop.status === 'info',
                        }"
                      ></span>
                      <span
                        v-if="idx < buildRouteHops(r).length - 1"
                        class="topo-seg"
                        :class="{
                          err: hop.status === 'err',
                          warn: hop.status === 'warn',
                        }"
                      ></span>
                    </div>
                    <div class="topo-title">{{ hop.title }}</div>
                    <div class="topo-val tnum">{{ hop.val }}</div>
                  </div>
                </div>

                <div
                  v-if="r.last_error"
                  class="topo-alert"
                  :class="r.lifecycle_state === 'issuing_tls' ? 'info' : r.lifecycle_state === 'config_drift' ? 'warn' : 'err'"
                >
                  <span>{{ r.last_error }}</span>
                  <button
                    v-if="r.lifecycle_state === 'config_drift'"
                    type="button"
                    class="btn-outline"
                    style="min-height:30px;padding:0 10px;font-size:12px;"
                    @click.stop="quickHealDrift(r.edge_node_id)"
                  >
                    同步配置
                  </button>
                </div>
              </div>
            </article>
          </div>

          <!-- Table View -->
          <div v-else class="swiss-table-wrap">
            <table class="swiss-table">
              <thead>
                <tr>
                  <th>域名</th>
                  <th>本地目标</th>
                  <th>转发方式</th>
                  <th>边缘节点</th>
                  <th>证书到期</th>
                  <th>CDN</th>
                  <th>状态</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="r in filteredRoutes" :key="r.id" @click="inspectedRouteId = r.id">
                  <td><strong>{{ r.full_domain }}</strong></td>
                  <td class="tnum">{{ r.local_target_host }}:{{ r.local_target_port }}</td>
                  <td>{{ r.forward_mode === 'relay' ? 'Relay' : 'Direct' }}</td>
                  <td class="tnum">{{ r.edge_node_name }}</td>
                  <td class="tnum">{{ r.origin_cert_days >= 0 ? `${r.origin_cert_days} 天` : '签发中' }}</td>
                  <td>{{ r.dns_proxied ? '开启' : '关闭' }}</td>
                  <td>
                    <span class="badge" :class="stateBadge(r.lifecycle_state).cls">
                      {{ stateBadge(r.lifecycle_state).label }}
                    </span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </template>
      </section>

      <!-- VIEW 2: EDGE NODES -->
      <section v-else-if="activeView === 'nodes'">
        <div class="page-head">
          <h1 class="page-title">边缘节点</h1>
          <button type="button" class="btn-primary" @click="showNodeModal = true">
            添加节点
          </button>
        </div>

        <div v-if="dash.nodes.length === 0" class="empty-state">
          <h2 style="font-size:18px;font-weight:700;">尚未添加边缘节点</h2>
          <button type="button" class="btn-primary" @click="showNodeModal = true">添加节点</button>
        </div>

        <div v-else class="panel-grid">
          <div v-for="n in dash.nodes" :key="n.id" class="panel-card">
            <div style="display:flex;align-items:flex-start;justify-content:space-between;gap:10px;">
              <div>
                <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
                  <h2 style="font-size:18px;font-weight:700;">{{ n.name }}</h2>
                  <span v-if="n.region" class="badge">{{ n.region }}</span>
                  <span v-if="n.is_default" class="badge info">默认</span>
                </div>
                <p class="tnum" style="font-size:14px;color:var(--text-secondary);margin-top:4px;">
                  {{ n.caddy_admin_url }}
                </p>
              </div>
              <span
                class="badge"
                :class="n.health_status === 'online' ? 'ok' : n.health_status === 'offline' ? 'err' : 'warn'"
              >
                {{ n.health_status.toUpperCase() }}
              </span>
            </div>

            <div class="kv-grid">
              <div>
                <div class="kv-label">公网 IPv4</div>
                <div class="kv-val tnum">{{ n.public_ipv4 }}</div>
              </div>
              <div>
                <div class="kv-label">Tailscale</div>
                <div class="kv-val tnum">{{ n.tailscale_ip }} ({{ n.ts_rtt_ms >= 0 ? `${n.ts_rtt_ms}ms` : '--' }})</div>
              </div>
              <div>
                <div class="kv-label">路由数</div>
                <div class="kv-val tnum">{{ n.route_count }}</div>
              </div>
            </div>

            <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;flex-wrap:wrap;">
              <div style="display:flex;gap:8px;flex-wrap:wrap;">
                <button
                  type="button"
                  class="btn-outline"
                  @click="api.pingNode(n.id).then(() => { refreshDashboard(); showToast(`已检测 ${n.name}`) })"
                >
                  测试连接
                </button>
                <button
                  type="button"
                  class="btn-outline"
                  @click="quickHealDrift(n.id)"
                >
                  同步路由
                </button>
                <button
                  v-if="!n.is_default"
                  type="button"
                  class="btn-outline"
                  @click="api.setDefaultNode(n.id).then(() => { refreshDashboard(); showToast(`已设 ${n.name} 为默认`) })"
                >
                  设为默认
                </button>
              </div>
              <button
                v-if="n.route_count === 0"
                type="button"
                class="btn-danger"
                @click="api.deleteNode(n.id).then(() => { refreshDashboard(); showToast(`已删除 ${n.name}`) }).catch((e) => showToast(e.message))"
              >
                删除
              </button>
            </div>
          </div>
        </div>
      </section>

      <!-- VIEW 3: DNS & ZONES -->
      <section v-else-if="activeView === 'dns'">
        <div class="page-head">
          <h1 class="page-title">域名与 DNS</h1>
          <button type="button" class="btn-primary" @click="showDNSModal = true">
            添加凭据
          </button>
        </div>

        <div v-if="dash.providers.length > 0" class="panel-grid" style="margin-bottom:20px;">
          <div v-for="p in dash.providers" :key="p.id" class="panel-card">
            <div style="display:flex;align-items:center;justify-content:space-between;gap:10px;">
              <div>
                <h2 style="font-size:17px;font-weight:700;">{{ p.name }}</h2>
                <p class="tnum" style="font-size:14px;color:var(--text-secondary);margin-top:3px;">
                  {{ p.provider_type.toUpperCase() }} | {{ p.masked_cred }}
                </p>
              </div>
              <span class="badge ok">正常</span>
            </div>
            <div style="display:flex;gap:8px;justify-content:flex-end;">
              <button
                type="button"
                class="btn-outline"
                @click="api.syncDNSProvider(p.id).then(() => { refreshDashboard(); showToast('已同步域名') })"
              >
                同步域名
              </button>
              <button
                type="button"
                class="btn-danger"
                @click="api.deleteDNSProvider(p.id).then(() => { refreshDashboard(); showToast('已删除凭据') }).catch((e) => showToast(e.message))"
              >
                删除
              </button>
            </div>
          </div>
        </div>

        <div class="swiss-table-wrap">
          <table class="swiss-table">
            <thead>
              <tr>
                <th>根域名</th>
                <th>DNS 凭据</th>
                <th>CDN 支持</th>
                <th>路由数</th>
                <th>默认</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="z in dash.zones" :key="z.id">
                <td><strong>{{ z.zone_name }}</strong></td>
                <td>{{ z.provider_name }}</td>
                <td>{{ z.supports_proxy ? '支持' : '-' }}</td>
                <td class="tnum">{{ z.route_count }}</td>
                <td>
                  <span v-if="z.is_default" class="badge info">默认</span>
                  <button
                    v-else
                    type="button"
                    class="btn-outline"
                    style="min-height:32px;padding:0 10px;font-size:12px;"
                    @click="api.setDefaultZone(z.id).then(() => { refreshDashboard(); showToast(`已设 ${z.zone_name} 为默认`) })"
                  >
                    设为默认
                  </button>
                </td>
              </tr>
              <tr v-if="dash.zones.length === 0">
                <td colspan="5" style="text-align:center;color:var(--text-secondary);padding:28px;">
                  暂无域名，请先添加 Cloudflare 或 DNSPod 凭据。
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <!-- VIEW 4: SETTINGS -->
      <section v-else-if="activeView === 'cli'">
        <div class="page-head">
          <h1 class="page-title">设置</h1>
          <div style="display:flex;gap:8px;">
            <button type="button" class="btn-outline" :disabled="doctorBusy" @click="runDoctor">
              {{ doctorBusy ? '检测中...' : '系统自检' }}
            </button>
            <button type="button" class="btn-outline" @click="handleLogout">
              退出登录
            </button>
          </div>
        </div>

        <div v-if="doctorChecks.length > 0" class="panel-card" style="margin-bottom:18px;">
          <h2 style="font-size:17px;font-weight:700;">系统自检 (vantu doctor)</h2>
          <div class="step-list">
            <div
              v-for="chk in doctorChecks"
              :key="chk.component"
              class="step-row"
              :class="chk.status === 'ok' ? 'done' : chk.status === 'error' ? 'error' : 'running'"
            >
              <span>{{ chk.component }}</span>
              <span>{{ chk.detail }}</span>
            </div>
          </div>
        </div>

        <!-- Version & Update -->
        <div class="panel-card" style="margin-bottom:18px;">
          <div style="display:flex;align-items:flex-start;justify-content:space-between;gap:10px;flex-wrap:wrap;">
            <div>
              <h2 style="font-size:17px;font-weight:700;">版本与更新</h2>
              <p class="tnum" style="font-size:13px;color:var(--text-secondary);margin-top:2px;">
                当前 {{ version }}
                <template v-if="updateInfo?.latest_version"> · 最新发布 {{ updateInfo.latest_version }}</template>
                <template v-if="updateInfo?.checked_at"> · 检查于 {{ new Date(updateInfo.checked_at).toLocaleString() }}</template>
              </p>
            </div>
            <div style="display:flex;gap:8px;flex-wrap:wrap;">
              <button type="button" class="btn-outline" :disabled="updateChecking || updateApplying" @click="loadUpdateInfo(true)">
                {{ updateChecking ? '检查中...' : '检查更新' }}
              </button>
              <button
                v-if="updateInfo?.has_update && updateInfo.can_self_update"
                type="button"
                class="btn-primary"
                :disabled="updateApplying"
                @click="handleApplyUpdate"
              >
                {{ updateApplying ? '更新中...' : `更新至 ${updateInfo.latest_version}` }}
              </button>
            </div>
          </div>

          <div v-if="updateInfo?.check_error" style="font-size:13px;color:var(--text-secondary);">
            {{ updateInfo.check_error }}
          </div>

          <template v-if="updateInfo?.has_update">
            <div v-if="updateInfo.upgrade_command" style="display:flex;flex-direction:column;gap:6px;">
              <div style="font-size:13px;color:var(--text-secondary);">
                {{ updateInfo.self_update_note }}，在 docker-compose.yml 所在目录执行：
              </div>
              <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
                <code class="code-block" style="flex:1;margin:0;font-size:12.5px;">{{ updateInfo.upgrade_command }}</code>
                <button type="button" class="btn-outline" @click="copyText(updateInfo.upgrade_command, '升级命令')">复制</button>
              </div>
            </div>
            <div v-else-if="!updateInfo.can_self_update" style="font-size:13px;color:var(--warn-fg);">
              {{ updateInfo.self_update_note }}
            </div>
            <details v-if="updateInfo.release_notes">
              <summary style="cursor:pointer;font-size:13px;font-weight:600;">更新说明</summary>
              <pre class="code-block" style="max-height:200px;overflow-y:auto;font-size:12.5px;white-space:pre-wrap;">{{ updateInfo.release_notes }}</pre>
            </details>
            <a :href="updateInfo.release_url" target="_blank" rel="noopener" style="font-size:13px;color:var(--accent-focus);">
              在 GitHub 查看发布页 →
            </a>
          </template>
        </div>

        <div class="panel-grid" style="margin-bottom:18px;">
          <!-- API Tokens -->
          <div class="panel-card">
            <h2 style="font-size:17px;font-weight:700;">API Token</h2>
            <div style="display:flex;gap:8px;flex-wrap:wrap;">
              <input v-model="newTokenName" class="input" style="flex:1;" type="text" placeholder="名称" />
              <select v-model="newTokenScope" class="select" aria-label="权限范围">
                <option value="admin">读写 (admin)</option>
                <option value="readonly">只读 (readonly)</option>
              </select>
              <button type="button" class="btn-primary" @click="handleCreateToken">创建</button>
            </div>

            <div
              v-if="createdPlainToken"
              style="padding:12px;border-radius:8px;border:1px solid var(--ok-border);background:var(--ok-bg);"
            >
              <div style="font-size:12px;color:var(--ok-fg);font-weight:600;margin-bottom:4px;">
                请妥善保存（仅显示一次）：
              </div>
              <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;flex-wrap:wrap;">
                <code class="tnum" style="font-family:var(--font-code);font-size:13px;word-break:break-all;">{{ createdPlainToken }}</code>
                <button type="button" class="btn-outline" @click="copyText(createdPlainToken, ' Token')">复制</button>
              </div>
            </div>

            <div class="step-list">
              <div v-for="t in dash.tokens" :key="t.id" class="step-row">
                <span>
                  <strong>{{ t.name }}</strong>
                  <span class="tnum" style="margin-left:8px;font-size:12px;">{{ t.token_prefix }}... ({{ t.scope }})</span>
                </span>
                <button
                  type="button"
                  style="color:var(--err-fg);font-size:13px;font-weight:600;"
                  @click="handleDeleteToken(t.id)"
                >
                  删除
                </button>
              </div>
              <div v-if="dash.tokens.length === 0" style="font-size:14px;color:var(--text-secondary);">
                暂无 API Token
              </div>
            </div>
          </div>

          <!-- AI Assistant Prompt -->
          <div class="panel-card">
            <div style="display:flex;align-items:flex-start;justify-content:space-between;gap:10px;flex-wrap:wrap;">
              <div>
                <h2 style="font-size:17px;font-weight:700;">AI 助手操控提示词</h2>
                <p style="font-size:13px;color:var(--text-secondary);margin-top:2px;">
                  不含明文密钥。粘贴给大模型后，AI 会优先本机免密执行，或引导你在本地终端配置凭据
                </p>
              </div>
              <button
                type="button"
                class="btn-primary"
                @click="copyText(aiPromptText, ' AI 提示词')"
              >
                复制提示词
              </button>
            </div>
            <pre class="code-block" style="max-height:220px;overflow-y:auto;font-size:12.5px;">{{ aiPromptText }}</pre>
          </div>
        </div>

        <!-- Password Update & Audit Log -->
        <div class="panel-grid">
          <form class="panel-card" @submit.prevent="handleChangePassword">
            <h2 style="font-size:17px;font-weight:700;">修改密码</h2>
            <div class="field">
              <label class="field-label" for="curPwdIn">当前密码</label>
              <input id="curPwdIn" v-model="curPassword" class="input" type="password" required />
            </div>
            <div class="field">
              <label class="field-label" for="newPwdIn">新密码</label>
              <input id="newPwdIn" v-model="nextPassword" class="input" type="password" required />
            </div>
            <button type="submit" class="btn-outline" style="align-self:flex-start;">保存密码</button>
          </form>

          <div class="panel-card">
            <h2 style="font-size:17px;font-weight:700;">操作记录</h2>
            <div class="step-list" style="max-height:240px;overflow-y:auto;">
              <div v-for="ev in dash.events.slice(0, 12)" :key="ev.id" class="step-row">
                <span>{{ ev.message }}</span>
                <span class="tnum" style="font-size:12px;white-space:nowrap;">{{ ev.actor }}</span>
              </div>
              <div v-if="dash.events.length === 0" style="font-size:14px;color:var(--text-secondary);">
                暂无记录
              </div>
            </div>
          </div>
        </div>
      </section>
    </main>

    <!-- Modals & Drawers -->
    <PublishModal
      v-if="showPublishModal && dash"
      :containers="dash.containers"
      :zones="dash.zones"
      :nodes="dash.nodes"
      :docker-available="dash.capabilities.docker_available"
      :initial-container="publishPresetContainer"
      :live-steps="livePublishSteps"
      @close="showPublishModal = false"
      @published="(m) => { showPublishModal = false; refreshDashboard(); showToast(m) }"
      @open-dns="showPublishModal = false; showDNSModal = true"
      @open-node="showPublishModal = false; showNodeModal = true"
    />

    <InspectorDrawer
      :route-id="inspectedRouteId"
      @close="inspectedRouteId = null"
      @updated="(m) => { refreshDashboard(); showToast(m) }"
    />

    <NodeModal
      v-if="showNodeModal"
      @close="showNodeModal = false"
      @saved="(m) => { showNodeModal = false; refreshDashboard(); showToast(m) }"
      @toast="showToast"
    />

    <DNSModal
      v-if="showDNSModal"
      @close="showDNSModal = false"
      @saved="(m) => { showDNSModal = false; refreshDashboard(); showToast(m) }"
    />

    <CommandPalette
      v-if="showCmdPalette && dash"
      :routes="dash.routes"
      :containers="dash.containers"
      :nodes="dash.nodes"
      @close="showCmdPalette = false"
      @inspect-route="(id) => (inspectedRouteId = id)"
      @publish-container="(c) => openPublish(c)"
      @open-publish="openPublish()"
      @switch-view="(v) => switchToView(v)"
      @toggle-theme="toggleTheme"
    />

    <div v-if="toastText" class="toast" role="status" aria-live="polite">
      {{ toastText }}
    </div>
  </div>
</template>
