<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
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
import { formatDateTime, locale, t, toggleLocale } from './i18n'
import { buildAIPrompt } from './aiPrompt'

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
const proxyBusyMap = ref<Record<string, boolean>>({})
// Latest publish step per domain (from SSE) and routes whose deletion is in flight.
const routeLiveStep = ref<Record<string, StepProgress>>({})
const deletingRoutes = ref<Record<string, boolean>>({})

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
      if (d.domain) {
        if (d.step === 'probe_verify' && d.status !== 'running') {
          delete routeLiveStep.value[d.domain]
        } else {
          routeLiveStep.value[d.domain] = d
        }
      }
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

// Backend messages are localized server-side, so refetch everything after a language switch.
watch(locale, () => {
  if (!authenticated.value) return
  doctorChecks.value = []
  refreshDashboard()
  loadUpdateInfo()
  connectSSE()
})

const unexposedList = computed(() =>
  (dash.value?.containers || []).filter((c) => c.published_domains.length === 0)
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
    title: r.source_type === 'docker' ? t('hop.container') : r.source_type === 'self' ? t('hop.self') : t('hop.local'),
    val: `:${r.local_target_port} ${r.local_healthy ? t('hop.online') : t('hop.offline')}`,
    status: r.local_healthy ? 'ok' : 'err',
  })

  if (r.forward_mode === 'relay') {
    hops.push({
      key: 'relay',
      title: t('hop.relay'),
      val: 'Local Relay',
      status: 'ok',
    })
  }

  hops.push({
    key: 'edge',
    title: r.dns_proxied ? t('hop.edgeOrigin') : t('hop.edgeNode'),
    val: `${r.edge_node_name}${r.edge_node_ts_rtt_ms >= 0 ? ` (${r.edge_node_ts_rtt_ms}ms)` : ''}`,
    status: !r.caddy_synced ? 'warn' : r.edge_node_ts_type === 'derp' ? 'warn' : 'ok',
  })

  if (r.dns_proxied) {
    hops.push({
      key: 'cdn',
      title: 'Cloudflare',
      val: r.origin_cert_days >= 0 ? t('hop.originCert', { n: r.origin_cert_days }) : t('hop.originIssuing'),
      status: !r.origin_tls_ok ? 'info' : r.origin_cert_days < 14 ? 'warn' : 'ok',
    })
  }

  const pubVal = r.dns_proxied
    ? r.public_http_code > 0
      ? `${r.public_http_code} (${r.public_latency_ms}ms)`
      : t('hop.probing')
    : r.public_http_code > 0
    ? `${r.public_http_code}${r.origin_cert_days >= 0 ? ` (${t('hop.daysShort', { n: r.origin_cert_days })})` : ''}`
    : r.origin_cert_days >= 0
    ? t('hop.cert', { n: r.origin_cert_days })
    : t('hop.issuing')

  hops.push({
    key: 'public',
    title: t('hop.public'),
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

/** Transient label shown while a route is being configured or removed; empty when idle. */
function routeBusyText(r: RouteRecord): string {
  if (deletingRoutes.value[r.id]) return t('state.deleting')
  const step = routeLiveStep.value[r.full_domain]
  if (step && step.status === 'running') return t('state.configuringStep', { step: step.title })
  if (r.lifecycle_state === 'provisioning' || r.lifecycle_state === 'planned') return t('state.configuring')
  return ''
}

function inspectDomain(domain: string) {
  const r = dash.value?.routes.find((x) => x.full_domain === domain)
  if (!r) return
  showPublishModal.value = false
  inspectedRouteId.value = r.id
}

async function deleteRouteOptimistic(id: string, domain: string, deleteDNS: boolean) {
  deletingRoutes.value[id] = true
  try {
    await api.deleteRoute(id, deleteDNS)
    await refreshDashboard()
    showToast(t('common.deletedName', { name: domain }))
  } catch (err) {
    showToast((err as Error).message)
  } finally {
    delete deletingRoutes.value[id]
  }
}

function stateBadge(state: string): { label: string; cls: string } {
  switch (state) {
    case 'provisioning':
    case 'planned':
      return { label: t('state.configuring'), cls: 'info' }
    case 'active':
      return { label: t('state.active'), cls: 'ok' }
    case 'issuing_tls':
      return { label: t('state.issuing'), cls: 'info' }
    case 'config_drift':
      return { label: t('state.drift'), cls: 'warn' }
    case 'paused':
      return { label: t('state.paused'), cls: '' }
    default:
      return { label: t('state.error'), cls: 'err' }
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
    showToast(t(updated.dns_proxied ? 'dash.cdnOn' : 'dash.cdnOff', { domain: updated.full_domain }))
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
    showToast(t('dash.syncedRoutes', { n: res.pushed_routes }))
  } catch (err) {
    showToast((err as Error).message)
  }
}

async function triggerGlobalProbe() {
  if (!dash.value) return
  showToast(t('dash.probing'))
  try {
    await Promise.all(dash.value.nodes.map((n) => api.pingNode(n.id)))
    await Promise.all(dash.value.routes.map((r) => api.probeRoute(r.id)))
    await refreshDashboard()
    showToast(t('dash.probeDone'))
  } catch (err) {
    showToast((err as Error).message)
  }
}

function switchToView(v: 'dashboard' | 'nodes' | 'dns' | 'cli') {
  activeView.value = v
}

const aiPromptText = computed(() =>
  buildAIPrompt(locale.value, window.location.origin, dash.value, unexposedList.value)
)

async function handleCreateToken() {
  try {
    const res = await api.createToken(newTokenName.value, newTokenScope.value)
    createdPlainToken.value = res.plain_text
    await refreshDashboard()
    showToast(t('settings.tokenCreated'))
  } catch (err) {
    showToast((err as Error).message)
  }
}

async function handleDeleteToken(id: string) {
  try {
    await api.deleteToken(id)
    await refreshDashboard()
    showToast(t('settings.tokenDeleted'))
  } catch (err) {
    showToast((err as Error).message)
  }
}

async function runDoctor() {
  doctorBusy.value = true
  try {
    const rep = await api.getDoctor()
    doctorChecks.value = rep.checks
    showToast(rep.healthy ? t('settings.doctorOK') : t('settings.doctorBad'))
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
    showToast(t('settings.pwdChanged'))
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
      showToast(t('update.upToDate'))
    }
  } catch (err) {
    if (refresh) showToast((err as Error).message)
  } finally {
    updateChecking.value = false
  }
}

async function handleApplyUpdate() {
  const target = updateInfo.value?.latest_version
  if (!target || !confirm(t('update.confirm', { version: target }))) return
  updateApplying.value = true
  try {
    await api.applyUpdate()
    showToast(t('update.waiting', { version: target }))
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
    showToast(t('update.timeout'))
  } catch (err) {
    showToast((err as Error).message)
  } finally {
    updateApplying.value = false
  }
}

function copyText(text: string, label: string) {
  navigator.clipboard?.writeText(text)
  showToast(t('common.copied', { label }))
}
</script>

<template>
  <div v-if="bootLoading" class="auth-shell">
    <p style="font-size:15px;color:var(--text-secondary);">{{ t('common.loading') }}</p>
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
          <span class="brand-title">{{ t('common.appName') }}</span>
          <span class="brand-tag tnum">{{ version }}</span>
          <button
            v-if="updateInfo?.has_update"
            type="button"
            class="update-pill tnum"
            :title="t('nav.updatePillTitle', { version: updateInfo.latest_version })"
            @click="switchToView('cli')"
          >
            {{ t('nav.updatePill', { version: updateInfo.latest_version }) }}
          </button>
        </div>

        <nav class="nav-tabs" :aria-label="t('nav.main')">
          <button
            type="button"
            class="nav-tab"
            :class="{ active: activeView === 'dashboard' }"
            @click="switchToView('dashboard')"
          >
            {{ t('nav.routes') }} <span class="nav-count tnum">{{ dash?.routes.length || 0 }}</span>
          </button>
          <button
            type="button"
            class="nav-tab"
            :class="{ active: activeView === 'nodes' }"
            @click="switchToView('nodes')"
          >
            {{ t('nav.nodes') }} <span class="nav-count tnum">{{ dash?.nodes.length || 0 }}</span>
          </button>
          <button
            type="button"
            class="nav-tab"
            :class="{ active: activeView === 'dns' }"
            @click="switchToView('dns')"
          >
            {{ t('nav.domains') }} <span class="nav-count tnum">{{ dash?.zones.length || 0 }}</span>
          </button>
          <button
            type="button"
            class="nav-tab"
            :class="{ active: activeView === 'cli' }"
            @click="switchToView('cli')"
          >
            {{ t('nav.settings') }}
          </button>
        </nav>

        <div class="top-actions">
          <button type="button" class="cmd-trigger" @click="showCmdPalette = true" :aria-label="t('nav.quickSearch')">
            <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
              <circle cx="11" cy="11" r="8"/><path d="m21 21-4.3-4.3"/>
            </svg>
            <span class="cmd-label">{{ t('nav.search') }}</span>
            <span class="kbd">⌘K</span>
          </button>
          <a
            class="icon-btn"
            href="https://github.com/llanc/vantu"
            target="_blank"
            rel="noopener"
            :aria-label="t('common.github')"
            :title="t('common.github')"
          >
            <svg width="17" height="17" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
              <path d="M12 .5C5.65.5.5 5.65.5 12c0 5.08 3.29 9.39 7.86 10.91.58.1.79-.25.79-.56v-2c-3.2.7-3.87-1.37-3.87-1.37-.53-1.33-1.28-1.69-1.28-1.69-1.05-.72.08-.7.08-.7 1.16.08 1.77 1.19 1.77 1.19 1.03 1.77 2.7 1.26 3.36.96.1-.75.4-1.26.73-1.55-2.55-.29-5.24-1.28-5.24-5.69 0-1.26.45-2.29 1.19-3.1-.12-.29-.52-1.46.11-3.05 0 0 .97-.31 3.17 1.18a11 11 0 0 1 5.77 0c2.2-1.49 3.17-1.18 3.17-1.18.63 1.59.23 2.76.11 3.05.74.81 1.19 1.84 1.19 3.1 0 4.42-2.69 5.39-5.26 5.68.41.36.78 1.06.78 2.14v3.17c0 .31.21.67.8.56A11.5 11.5 0 0 0 23.5 12C23.5 5.65 18.35.5 12 .5Z"/>
            </svg>
          </a>
          <button
            type="button"
            class="icon-btn lang-btn"
            :aria-label="t('common.toggleLang')"
            :title="t('common.toggleLang')"
            @click="toggleLocale"
          >
            {{ t('common.langShort') }}
          </button>
          <button
            type="button"
            class="icon-btn"
            :aria-label="t('common.toggleTheme')"
            :title="t('common.toggleTheme')"
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
            {{ t('nav.publish') }}
          </button>
        </div>
      </div>
    </header>

    <main v-if="dash" class="container">
      <!-- VIEW 1: ROUTES -->
      <section v-if="activeView === 'dashboard'">
        <div class="page-head">
          <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap;">
            <h1 class="page-title">{{ t('dash.title') }}</h1>
            <span v-if="dash.capabilities.tailscale.available" class="badge tnum">
              Tailscale {{ dash.capabilities.tailscale.ipv4 }}
            </span>
          </div>
          <div style="display:flex;gap:8px;flex-wrap:wrap;">
            <button v-if="dash.routes.length > 0" type="button" class="btn-outline" @click="triggerGlobalProbe">
              {{ t('dash.refresh') }}
            </button>
          </div>
        </div>

        <!-- Summary Strip -->
        <div class="summary-strip">
          <div class="summary-cell">
            <div class="summary-label"><span>{{ t('dash.onlineRoutes') }}</span></div>
            <div class="summary-value tnum">
              {{ dash.summary.active_routes }} / {{ dash.summary.total_routes }}
            </div>
            <div class="summary-foot">
              <span v-if="dash.summary.degraded_count > 0 || dash.summary.drift_count > 0" style="color:var(--err-fg);font-weight:500;">
                {{ t('dash.abnormalCount', { n: dash.summary.degraded_count + dash.summary.drift_count }) }}
              </span>
              <span v-else>{{ dash.summary.total_routes > 0 ? t('dash.running') : t('dash.noRoutes') }}</span>
            </div>
          </div>

          <div class="summary-cell">
            <div class="summary-label"><span>{{ t('dash.unexposed') }}</span></div>
            <button
              v-if="unexposedList.length > 0"
              type="button"
              class="summary-value summary-value-btn tnum"
              :title="t('dash.unexposedTitle')"
              @click="openPublish(unexposedList[0])"
            >
              {{ unexposedList.length }}
              <svg class="summary-value-arrow" width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                <path d="M5 12h14M13 6l6 6-6 6"/>
              </svg>
            </button>
            <div v-else class="summary-value tnum">0</div>
            <div class="summary-foot">
              <span>
                {{
                  unexposedList.length > 0
                    ? t('dash.unexposedHint')
                    : dash.capabilities.docker_available ? t('dash.noUnexposed') : t('dash.noDocker')
                }}
              </span>
            </div>
          </div>

          <div class="summary-cell">
            <div class="summary-label"><span>{{ t('dash.edgeNodes') }}</span></div>
            <div class="summary-value tnum">
              {{ dash.summary.nodes_online }} / {{ dash.summary.nodes_total }}
            </div>
            <div class="summary-foot">
              <span v-if="dash.nodes.length > 0" class="tnum">
                {{ t('dash.defaultNode', { name: dash.nodes[0].name }) }}
              </span>
              <button v-else type="button" class="discover-chip" @click="showNodeModal = true">
                {{ t('dash.addNode') }}
              </button>
            </div>
          </div>

          <div class="summary-cell">
            <div class="summary-label"><span>{{ t('dash.certExpiry') }}</span></div>
            <div class="summary-value tnum">
              {{ dash.summary.min_cert_days >= 0 ? t('dash.days', { n: dash.summary.min_cert_days }) : '--' }}
            </div>
            <div class="summary-foot">
              <span>{{ dash.summary.min_cert_domain || t('dash.noCert') }}</span>
            </div>
          </div>
        </div>

        <!-- Onboarding / Empty State -->
        <div v-if="dash.routes.length === 0" class="empty-state">
          <h2 style="font-size:18px;font-weight:700;">{{ t('dash.emptyTitle') }}</h2>
          <div style="display:flex;gap:10px;flex-wrap:wrap;justify-content:center;margin-top:6px;">
            <button v-if="dash.zones.length === 0" type="button" class="btn-outline" @click="showDNSModal = true">
              {{ t('dash.stepDNS') }}
            </button>
            <button v-if="dash.nodes.length === 0" type="button" class="btn-outline" @click="showNodeModal = true">
              {{ t('dash.stepNode') }}
            </button>
            <button type="button" class="btn-primary" @click="openPublish()">
              {{ t('nav.publish') }}
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
                  :placeholder="t('dash.searchPlaceholder')"
                  :aria-label="t('dash.searchLabel')"
                />
              </div>

              <select v-model="nodeFilter" class="select" :aria-label="t('dash.nodeFilter')">
                <option value="all">{{ t('dash.allNodes') }}</option>
                <option v-for="n in dash.nodes" :key="n.id" :value="n.id">{{ n.name }}</option>
              </select>

              <select v-model="statusFilter" class="select" :aria-label="t('dash.statusFilter')">
                <option value="all">{{ t('dash.allStatus') }}</option>
                <option value="healthy">{{ t('state.active') }}</option>
                <option value="attention">{{ t('dash.attention') }}</option>
              </select>
            </div>

            <div class="segmented" role="group" :aria-label="t('dash.viewSwitch')">
              <button type="button" class="seg-btn" :class="{ active: layoutMode === 'cards' }" @click="layoutMode = 'cards'">
                {{ t('dash.cards') }}
              </button>
              <button type="button" class="seg-btn" :class="{ active: layoutMode === 'table' }" @click="layoutMode = 'table'">
                {{ t('dash.table') }}
              </button>
            </div>
          </div>

          <!-- Cards Grid (Dynamic 3 / 4 / 5 Hops per Route!) -->
          <TransitionGroup v-if="layoutMode === 'cards'" name="route" tag="div" class="routes-grid">
            <article
              v-for="r in filteredRoutes"
              :key="r.id"
              class="route-card"
              :class="{
                broken: r.lifecycle_state === 'degraded' || r.lifecycle_state === 'error',
                busy: !!routeBusyText(r),
                deleting: !!deletingRoutes[r.id],
              }"
              @click="!deletingRoutes[r.id] && (inspectedRouteId = r.id)"
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
                    <span v-if="routeBusyText(r)" class="badge info busy-badge">
                      <span class="spinner" aria-hidden="true"></span>{{ routeBusyText(r) }}
                    </span>
                    <span v-else class="badge" :class="stateBadge(r.lifecycle_state).cls">
                      {{ stateBadge(r.lifecycle_state).label }}
                    </span>
                  </div>
                  <div class="route-submeta">
                    <span class="badge tnum">
                      {{
                        r.source_type === 'docker'
                          ? r.container_name
                          : r.source_type === 'self'
                          ? t('dash.selfConsole', { port: r.local_target_port })
                          : `${r.local_target_host}:${r.local_target_port}`
                      }}
                    </span>
                    <span class="badge">{{ r.edge_node_name }}</span>
                    <span v-if="r.preset !== 'web'" class="badge">{{ r.preset.toUpperCase() }}</span>
                    <span v-if="r.basic_auth_user" class="badge auth-badge" :title="t('route.authUserIs', { user: r.basic_auth_user })">
                      <svg width="11" height="11" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true">
                        <rect x="4" y="11" width="16" height="10" rx="2"/><path d="M8 11V7a4 4 0 0 1 8 0v4"/>
                      </svg>
                      {{ t('route.authBadge') }}
                    </span>
                  </div>
                </div>

                <button
                  v-if="r.supports_proxy"
                  type="button"
                  class="cf-switch"
                  :class="{ on: r.dns_proxied, busy: !!proxyBusyMap[r.id] }"
                  :disabled="!!proxyBusyMap[r.id]"
                  :title="proxyBusyMap[r.id] ? t('dash.cdnBusy') : r.dns_proxied ? t('dash.cdnTurnOff') : t('dash.cdnTurnOn')"
                  @click.stop="onToggleProxy(r)"
                >
                  <span>{{ proxyBusyMap[r.id] ? t('dash.cdnSwitching') : 'CDN' }}</span>
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
                    {{ t('dash.syncConfig') }}
                  </button>
                </div>
              </div>
            </article>
          </TransitionGroup>

          <!-- Table View -->
          <div v-else class="swiss-table-wrap">
            <table class="swiss-table">
              <thead>
                <tr>
                  <th>{{ t('dash.colDomain') }}</th>
                  <th>{{ t('dash.colTarget') }}</th>
                  <th>{{ t('dash.colMode') }}</th>
                  <th>{{ t('dash.colNode') }}</th>
                  <th>{{ t('dash.colCert') }}</th>
                  <th>CDN</th>
                  <th>{{ t('dash.colStatus') }}</th>
                </tr>
              </thead>
              <TransitionGroup name="row" tag="tbody">
                <tr
                  v-for="r in filteredRoutes"
                  :key="r.id"
                  :class="{ deleting: !!deletingRoutes[r.id] }"
                  @click="!deletingRoutes[r.id] && (inspectedRouteId = r.id)"
                >
                  <td>
                    <strong>{{ r.full_domain }}</strong>
                    <span v-if="r.basic_auth_user" class="badge auth-badge" style="margin-left:6px;">{{ t('route.authBadge') }}</span>
                  </td>
                  <td class="tnum">{{ r.local_target_host }}:{{ r.local_target_port }}</td>
                  <td>{{ r.forward_mode === 'relay' ? 'Relay' : 'Direct' }}</td>
                  <td class="tnum">{{ r.edge_node_name }}</td>
                  <td class="tnum">{{ r.origin_cert_days >= 0 ? t('dash.days', { n: r.origin_cert_days }) : t('dash.issuing') }}</td>
                  <td>{{ r.dns_proxied ? t('common.on') : t('common.off') }}</td>
                  <td>
                    <span v-if="routeBusyText(r)" class="badge info busy-badge">
                      <span class="spinner" aria-hidden="true"></span>{{ routeBusyText(r) }}
                    </span>
                    <span v-else class="badge" :class="stateBadge(r.lifecycle_state).cls">
                      {{ stateBadge(r.lifecycle_state).label }}
                    </span>
                  </td>
                </tr>
              </TransitionGroup>
            </table>
          </div>
        </template>
      </section>

      <!-- VIEW 2: EDGE NODES -->
      <section v-else-if="activeView === 'nodes'">
        <div class="page-head">
          <h1 class="page-title">{{ t('nodes.title') }}</h1>
          <button type="button" class="btn-primary" @click="showNodeModal = true">
            {{ t('nodes.add') }}
          </button>
        </div>

        <div v-if="dash.nodes.length === 0" class="empty-state">
          <h2 style="font-size:18px;font-weight:700;">{{ t('nodes.empty') }}</h2>
          <button type="button" class="btn-primary" @click="showNodeModal = true">{{ t('nodes.add') }}</button>
        </div>

        <div v-else class="panel-grid">
          <div v-for="n in dash.nodes" :key="n.id" class="panel-card">
            <div style="display:flex;align-items:flex-start;justify-content:space-between;gap:10px;">
              <div>
                <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
                  <h2 style="font-size:18px;font-weight:700;">{{ n.name }}</h2>
                  <span v-if="n.region" class="badge">{{ n.region }}</span>
                  <span v-if="n.is_default" class="badge info">{{ t('common.default') }}</span>
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
                <div class="kv-label">{{ t('nodes.publicIP') }}</div>
                <div class="kv-val tnum">{{ n.public_ipv4 }}</div>
              </div>
              <div>
                <div class="kv-label">Tailscale</div>
                <div class="kv-val tnum">{{ n.tailscale_ip }} ({{ n.ts_rtt_ms >= 0 ? `${n.ts_rtt_ms}ms` : '--' }})</div>
              </div>
              <div>
                <div class="kv-label">{{ t('nodes.routeCount') }}</div>
                <div class="kv-val tnum">{{ n.route_count }}</div>
              </div>
            </div>

            <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;flex-wrap:wrap;">
              <div style="display:flex;gap:8px;flex-wrap:wrap;">
                <button
                  type="button"
                  class="btn-outline"
                  @click="api.pingNode(n.id).then(() => { refreshDashboard(); showToast(t('nodes.tested', { name: n.name })) })"
                >
                  {{ t('nodes.test') }}
                </button>
                <button
                  type="button"
                  class="btn-outline"
                  @click="quickHealDrift(n.id)"
                >
                  {{ t('nodes.sync') }}
                </button>
                <button
                  v-if="!n.is_default"
                  type="button"
                  class="btn-outline"
                  @click="api.setDefaultNode(n.id).then(() => { refreshDashboard(); showToast(t('common.setDefaultDone', { name: n.name })) })"
                >
                  {{ t('common.setDefault') }}
                </button>
              </div>
              <button
                v-if="n.route_count === 0"
                type="button"
                class="btn-danger"
                @click="api.deleteNode(n.id).then(() => { refreshDashboard(); showToast(t('common.deletedName', { name: n.name })) }).catch((e) => showToast(e.message))"
              >
                {{ t('common.delete') }}
              </button>
            </div>
          </div>
        </div>
      </section>

      <!-- VIEW 3: DNS & ZONES -->
      <section v-else-if="activeView === 'dns'">
        <div class="page-head">
          <h1 class="page-title">{{ t('dns.title') }}</h1>
          <button type="button" class="btn-primary" @click="showDNSModal = true">
            {{ t('dns.add') }}
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
              <span class="badge ok">{{ t('common.ok') }}</span>
            </div>
            <div style="display:flex;gap:8px;justify-content:flex-end;">
              <button
                type="button"
                class="btn-outline"
                @click="api.syncDNSProvider(p.id).then(() => { refreshDashboard(); showToast(t('dns.synced')) })"
              >
                {{ t('dns.sync') }}
              </button>
              <button
                type="button"
                class="btn-danger"
                @click="api.deleteDNSProvider(p.id).then(() => { refreshDashboard(); showToast(t('dns.deleted')) }).catch((e) => showToast(e.message))"
              >
                {{ t('common.delete') }}
              </button>
            </div>
          </div>
        </div>

        <div class="swiss-table-wrap">
          <table class="swiss-table">
            <thead>
              <tr>
                <th>{{ t('dns.colZone') }}</th>
                <th>{{ t('dns.colProvider') }}</th>
                <th>{{ t('dns.colCDN') }}</th>
                <th>{{ t('dns.colRoutes') }}</th>
                <th>{{ t('dns.colDefault') }}</th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="z in dash.zones" :key="z.id">
                <td><strong>{{ z.zone_name }}</strong></td>
                <td>{{ z.provider_name }}</td>
                <td>{{ z.supports_proxy ? t('common.supported') : '-' }}</td>
                <td class="tnum">{{ z.route_count }}</td>
                <td>
                  <span v-if="z.is_default" class="badge info">{{ t('common.default') }}</span>
                  <button
                    v-else
                    type="button"
                    class="btn-outline"
                    style="min-height:32px;padding:0 10px;font-size:12px;"
                    @click="api.setDefaultZone(z.id).then(() => { refreshDashboard(); showToast(t('common.setDefaultDone', { name: z.zone_name })) })"
                  >
                    {{ t('common.setDefault') }}
                  </button>
                </td>
              </tr>
              <tr v-if="dash.zones.length === 0">
                <td colspan="5" style="text-align:center;color:var(--text-secondary);padding:28px;">
                  {{ t('dns.empty') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <!-- VIEW 4: SETTINGS -->
      <section v-else-if="activeView === 'cli'">
        <div class="page-head">
          <h1 class="page-title">{{ t('settings.title') }}</h1>
          <div style="display:flex;gap:8px;">
            <button type="button" class="btn-outline" :disabled="doctorBusy" @click="runDoctor">
              {{ doctorBusy ? t('settings.doctorBusy') : t('settings.doctor') }}
            </button>
            <button type="button" class="btn-outline" @click="handleLogout">
              {{ t('settings.logout') }}
            </button>
          </div>
        </div>

        <div v-if="doctorChecks.length > 0" class="panel-card" style="margin-bottom:18px;">
          <h2 style="font-size:17px;font-weight:700;">{{ t('settings.doctorTitle') }}</h2>
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
              <h2 style="font-size:17px;font-weight:700;">{{ t('update.title') }}</h2>
              <p class="tnum" style="font-size:13px;color:var(--text-secondary);margin-top:2px;">
                {{ t('update.current', { version }) }}<template v-if="updateInfo?.latest_version">{{ t('update.latest', { version: updateInfo.latest_version }) }}</template><template v-if="updateInfo?.checked_at">{{ t('update.checkedAt', { time: formatDateTime(updateInfo.checked_at) }) }}</template>
              </p>
            </div>
            <div style="display:flex;gap:8px;flex-wrap:wrap;">
              <button type="button" class="btn-outline" :disabled="updateChecking || updateApplying" @click="loadUpdateInfo(true)">
                {{ updateChecking ? t('update.checking') : t('update.check') }}
              </button>
              <button
                v-if="updateInfo?.has_update && updateInfo.can_self_update"
                type="button"
                class="btn-primary"
                :disabled="updateApplying"
                @click="handleApplyUpdate"
              >
                {{ updateApplying ? t('update.applying') : t('update.applyTo', { version: updateInfo.latest_version }) }}
              </button>
            </div>
          </div>

          <div v-if="updateInfo?.check_error" style="font-size:13px;color:var(--text-secondary);">
            {{ updateInfo.check_error }}
          </div>

          <template v-if="updateInfo?.has_update">
            <div v-if="updateInfo.upgrade_command" style="display:flex;flex-direction:column;gap:6px;">
              <div style="font-size:13px;color:var(--text-secondary);">
                {{ t('update.dockerHint') }}
              </div>
              <div style="display:flex;align-items:center;gap:8px;flex-wrap:wrap;">
                <code class="code-block" style="flex:1;margin:0;font-size:12.5px;">{{ updateInfo.upgrade_command }}</code>
                <button type="button" class="btn-outline" @click="copyText(updateInfo.upgrade_command, t('update.commandLabel'))">{{ t('common.copy') }}</button>
              </div>
            </div>
            <div v-else-if="!updateInfo.can_self_update" style="font-size:13px;color:var(--warn-fg);">
              {{ updateInfo.self_update_note }}
            </div>
            <details v-if="updateInfo.release_notes">
              <summary style="cursor:pointer;font-size:13px;font-weight:600;">{{ t('update.notes') }}</summary>
              <pre class="code-block" style="max-height:200px;overflow-y:auto;font-size:12.5px;white-space:pre-wrap;">{{ updateInfo.release_notes }}</pre>
            </details>
            <a :href="updateInfo.release_url" target="_blank" rel="noopener" style="font-size:13px;color:var(--accent-focus);">
              {{ t('update.viewRelease') }}
            </a>
          </template>
        </div>

        <div class="panel-grid" style="margin-bottom:18px;">
          <!-- API Tokens -->
          <div class="panel-card">
            <h2 style="font-size:17px;font-weight:700;">API Token</h2>
            <div style="display:flex;gap:8px;flex-wrap:wrap;">
              <input v-model="newTokenName" class="input" style="flex:1;" type="text" :placeholder="t('settings.tokenName')" />
              <select v-model="newTokenScope" class="select" :aria-label="t('settings.tokenScope')">
                <option value="admin">{{ t('settings.scopeAdmin') }}</option>
                <option value="readonly">{{ t('settings.scopeReadonly') }}</option>
              </select>
              <button type="button" class="btn-primary" @click="handleCreateToken">{{ t('settings.create') }}</button>
            </div>

            <div
              v-if="createdPlainToken"
              style="padding:12px;border-radius:8px;border:1px solid var(--ok-border);background:var(--ok-bg);"
            >
              <div style="font-size:12px;color:var(--ok-fg);font-weight:600;margin-bottom:4px;">
                {{ t('settings.saveOnce') }}
              </div>
              <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;flex-wrap:wrap;">
                <code class="tnum" style="font-family:var(--font-code);font-size:13px;word-break:break-all;">{{ createdPlainToken }}</code>
                <button type="button" class="btn-outline" @click="copyText(createdPlainToken, t('settings.tokenLabel'))">{{ t('common.copy') }}</button>
              </div>
            </div>

            <div class="step-list">
              <div v-for="tk in dash.tokens" :key="tk.id" class="step-row">
                <span>
                  <strong>{{ tk.name }}</strong>
                  <span class="tnum" style="margin-left:8px;font-size:12px;">{{ tk.token_prefix }}... ({{ tk.scope }})</span>
                </span>
                <button
                  type="button"
                  style="color:var(--err-fg);font-size:13px;font-weight:600;"
                  @click="handleDeleteToken(tk.id)"
                >
                  {{ t('common.delete') }}
                </button>
              </div>
              <div v-if="dash.tokens.length === 0" style="font-size:14px;color:var(--text-secondary);">
                {{ t('settings.noTokens') }}
              </div>
            </div>
          </div>

          <!-- AI Assistant Prompt -->
          <div class="panel-card">
            <div style="display:flex;align-items:flex-start;justify-content:space-between;gap:10px;flex-wrap:wrap;">
              <div>
                <h2 style="font-size:17px;font-weight:700;">{{ t('settings.aiTitle') }}</h2>
                <p style="font-size:13px;color:var(--text-secondary);margin-top:2px;">
                  {{ t('settings.aiDesc') }}
                </p>
              </div>
              <button
                type="button"
                class="btn-primary"
                @click="copyText(aiPromptText, t('settings.aiLabel'))"
              >
                {{ t('settings.aiCopy') }}
              </button>
            </div>
            <pre class="code-block" style="max-height:220px;overflow-y:auto;font-size:12.5px;">{{ aiPromptText }}</pre>
          </div>
        </div>

        <!-- Password Update & Audit Log -->
        <div class="panel-grid">
          <form class="panel-card" @submit.prevent="handleChangePassword">
            <h2 style="font-size:17px;font-weight:700;">{{ t('settings.pwdTitle') }}</h2>
            <div class="field">
              <label class="field-label" for="curPwdIn">{{ t('settings.pwdCurrent') }}</label>
              <input id="curPwdIn" v-model="curPassword" class="input" type="password" required />
            </div>
            <div class="field">
              <label class="field-label" for="newPwdIn">{{ t('settings.pwdNew') }}</label>
              <input id="newPwdIn" v-model="nextPassword" class="input" type="password" required />
            </div>
            <button type="submit" class="btn-outline" style="align-self:flex-start;">{{ t('settings.pwdSave') }}</button>
          </form>

          <div class="panel-card">
            <h2 style="font-size:17px;font-weight:700;">{{ t('settings.auditTitle') }}</h2>
            <div class="step-list" style="max-height:240px;overflow-y:auto;">
              <div v-for="ev in dash.events.slice(0, 12)" :key="ev.id" class="step-row">
                <span>{{ ev.message }}</span>
                <span class="tnum" style="font-size:12px;white-space:nowrap;">{{ ev.actor }}</span>
              </div>
              <div v-if="dash.events.length === 0" style="font-size:14px;color:var(--text-secondary);">
                {{ t('settings.auditEmpty') }}
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
      @inspect-domain="inspectDomain"
      @open-node="showPublishModal = false; showNodeModal = true"
    />

    <InspectorDrawer
      :route-id="inspectedRouteId"
      :containers="dash?.containers ?? []"
      :nodes="dash?.nodes ?? []"
      @close="inspectedRouteId = null"
      @updated="(m) => { refreshDashboard(); showToast(m) }"
      @delete="deleteRouteOptimistic"
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
      @toggle-lang="toggleLocale"
    />

    <div v-if="toastText" class="toast" role="status" aria-live="polite">
      {{ toastText }}
    </div>
  </div>
</template>
