<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { api, APIRequestError, type DiscoveredContainer, type EdgeNode, type RouteDetail, type RouteUpdatePayload } from '../api'
import { locale, t } from '../i18n'
import { advancedFromRoute, emptyAdvanced, toAdvancedPayload, type AdvancedForm } from '../proxyForm'
import ProxyAdvancedFields from './ProxyAdvancedFields.vue'

const props = defineProps<{
  routeId: string | null
  containers: DiscoveredContainer[]
  nodes: EdgeNode[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'updated', msg: string): void
  (e: 'delete', id: string, domain: string, deleteDNS: boolean): void
}>()

const authEditing = ref(false)
const authUser = ref('')
const authPassword = ref('')

const detail = ref<RouteDetail | null>(null)
const loading = ref(false)
const actionBusy = ref(false)
const confirmDelete = ref(false)
const deleteDNS = ref(true)
const errMsg = ref('')

async function loadDetail() {
  if (!props.routeId) return
  loading.value = true
  errMsg.value = ''
  confirmDelete.value = false
  authEditing.value = false
  editing.value = false
  try {
    detail.value = await api.getRouteDetail(props.routeId)
  } catch (err) {
    errMsg.value = err instanceof APIRequestError ? err.message : (err as Error).message
  } finally {
    loading.value = false
  }
}

watch(() => props.routeId, loadDetail, { immediate: true })
watch(locale, loadDetail)

const paddedHistory = computed(() => {
  const hist = detail.value?.history || []
  const padCount = Math.max(0, 36 - hist.length)
  const empties = Array.from({ length: padCount }, (_, idx) => ({
    id: -idx - 1,
    empty: true,
    is_ok: true,
    public_latency_ms: 0,
    fail_stage: '',
  }))
  return [
    ...empties,
    ...hist.map((h) => ({
      id: h.id,
      empty: false,
      is_ok: h.is_ok,
      public_latency_ms: h.public_latency_ms,
      fail_stage: h.fail_stage,
    })),
  ]
})

async function runProbe() {
  if (!detail.value) return
  actionBusy.value = true
  errMsg.value = ''
  try {
    detail.value = await api.probeRoute(detail.value.route.id)
    emit('updated', t('inspect.probed', { domain: detail.value.route.full_domain }))
  } catch (err) {
    errMsg.value = err instanceof APIRequestError ? err.message : (err as Error).message
  } finally {
    actionBusy.value = false
  }
}

async function runRetry() {
  if (!detail.value) return
  actionBusy.value = true
  errMsg.value = ''
  try {
    await api.retryRoute(detail.value.route.id)
    detail.value = await api.getRouteDetail(detail.value.route.id)
    emit('updated', t('inspect.retried', { domain: detail.value.route.full_domain }))
  } catch (err) {
    errMsg.value = err instanceof APIRequestError ? err.message : (err as Error).message
  } finally {
    actionBusy.value = false
  }
}

async function toggleEnabled() {
  if (!detail.value) return
  actionBusy.value = true
  errMsg.value = ''
  try {
    const domain = detail.value.route.full_domain
    if (detail.value.route.enabled) {
      await api.pauseRoute(detail.value.route.id)
      emit('updated', t('inspect.paused', { domain }))
    } else {
      await api.resumeRoute(detail.value.route.id)
      emit('updated', t('inspect.resumed', { domain }))
    }
    detail.value = await api.getRouteDetail(detail.value.route.id)
  } catch (err) {
    errMsg.value = err instanceof APIRequestError ? err.message : (err as Error).message
  } finally {
    actionBusy.value = false
  }
}

function executeDelete() {
  if (!detail.value) return
  emit('delete', detail.value.route.id, detail.value.route.full_domain, deleteDNS.value)
  emit('close')
}

function startAuthEdit() {
  authUser.value = detail.value?.route.basic_auth_user || ''
  authPassword.value = ''
  authEditing.value = true
}

async function applyAuth(user: string, password: string) {
  if (!detail.value) return
  actionBusy.value = true
  errMsg.value = ''
  try {
    const rt = await api.setRouteAuth(detail.value.route.id, user, password)
    detail.value = await api.getRouteDetail(rt.id)
    authEditing.value = false
    emit('updated', t(user ? 'route.authOnDone' : 'route.authOffDone', { domain: rt.full_domain }))
  } catch (err) {
    errMsg.value = err instanceof APIRequestError ? err.message : (err as Error).message
  } finally {
    actionBusy.value = false
  }
}

function saveAuth() {
  if (!authUser.value.trim() || authPassword.value.length < 6) {
    errMsg.value = t('publish.authInvalid')
    return
  }
  applyAuth(authUser.value.trim(), authPassword.value)
}

// --- Service configuration editing ---
type Preset = 'web' | 'sse' | 'webdav' | 'https_insecure'
const editing = ref(false)
const applying = ref(false)
const edit = reactive({
  source: 'manual' as 'docker' | 'manual',
  container: '',
  containerPort: '' as number | '',
  scheme: 'http' as 'http' | 'https',
  host: '',
  port: 0 as number | '',
  node: '',
  preset: 'web' as Preset,
  mode: 'auto',
  note: '',
  skipCheck: false,
})
const editAdvanced = ref<AdvancedForm>(emptyAdvanced())

const presetOptions = computed(() => [
  { id: 'web', name: t('publish.presetWeb') },
  { id: 'sse', name: t('publish.presetSSE') },
  { id: 'webdav', name: t('publish.presetWebdav') },
  { id: 'https_insecure', name: t('publish.presetInsecure') },
])

const containerOptions = computed(() => {
  const names = props.containers.map((c) => c.name)
  const cur = detail.value?.route.container_name
  if (cur && !names.includes(cur)) names.unshift(cur)
  return names
})

const advancedChips = computed(() => {
  const r = detail.value?.route
  if (!r) return []
  const p = r.proxy ?? {}
  const chips: string[] = []
  if (r.max_body_mb > 0) chips.push(t('adv.chipBody', { n: r.max_body_mb }))
  chips.push(t('adv.chipTimeout', { n: r.read_timeout_sec }))
  if (p.upstream_host) chips.push(t('adv.chipHost', { v: p.upstream_host }))
  if (p.tls_skip_verify || r.preset === 'https_insecure') chips.push(t('adv.chipSkip'))
  if (p.compress) chips.push(t('adv.chipCompress'))
  if (p.ip_allowlist?.length) chips.push(t('adv.chipIP', { n: p.ip_allowlist.length }))
  const req = Object.keys(p.request_headers ?? {}).length
  if (req) chips.push(t('adv.chipReq', { n: req }))
  const resp = Object.keys(p.response_headers ?? {}).length
  if (resp) chips.push(t('adv.chipResp', { n: resp }))
  return chips
})

function startEdit() {
  const r = detail.value?.route
  if (!r) return
  edit.source = r.source_type === 'docker' ? 'docker' : 'manual'
  edit.container = r.container_name
  edit.containerPort = ''
  edit.scheme = r.upstream_scheme
  edit.host = r.local_target_host
  edit.port = r.local_target_port
  edit.node = r.edge_node_id
  edit.preset = r.preset
  edit.mode = r.forward_mode
  edit.note = r.note
  edit.skipCheck = false
  editAdvanced.value = advancedFromRoute(r)
  errMsg.value = ''
  editing.value = true
}

function targetChanged(): boolean {
  const r = detail.value!.route
  if (r.source_type === 'self') return false
  if (edit.source !== r.source_type) return true
  if (edit.source === 'docker') return edit.container !== r.container_name || edit.containerPort !== ''
  return edit.host.trim() !== r.local_target_host || Number(edit.port) !== r.local_target_port || edit.scheme !== r.upstream_scheme
}

async function saveEdit() {
  const r = detail.value?.route
  if (!r) return
  const { payload: adv, badLine } = toAdvancedPayload(editAdvanced.value)
  if (badLine) {
    errMsg.value = t('adv.badHeader', { line: badLine })
    return
  }
  const body: RouteUpdatePayload = {
    edge_node_id: edit.node,
    preset: edit.preset,
    forward_mode: edit.mode,
    note: edit.note.trim(),
    skip_local_check: edit.skipCheck,
    ...adv,
  }
  if (targetChanged()) {
    body.source_type = edit.source
    if (edit.source === 'docker') {
      body.container_name = edit.container
      body.local_port = Number(edit.containerPort) || undefined
      body.upstream_scheme = edit.preset === 'https_insecure' ? 'https' : r.upstream_scheme
    } else {
      body.local_host = edit.host.trim()
      body.local_port = Number(edit.port)
      body.upstream_scheme = edit.scheme
    }
  }
  // Applying re-runs DNS, Caddy and the probe; progress shows on the route card meanwhile.
  editing.value = false
  applying.value = true
  errMsg.value = ''
  try {
    const res = await api.updateRoute(r.id, body)
    if (props.routeId === r.id) detail.value = await api.getRouteDetail(r.id)
    emit('updated', t('edit.done', { domain: res.route.full_domain }))
  } catch (err) {
    errMsg.value = err instanceof APIRequestError ? `${err.message}${err.suggestion ? ' — ' + err.suggestion : ''}` : (err as Error).message
    editing.value = true
  } finally {
    applying.value = false
  }
}
</script>

<template>
  <div v-if="routeId" class="drawer-backdrop" @click.self="emit('close')">
    <aside class="drawer" role="dialog" aria-modal="true" :aria-label="t('inspect.label')">
      <div class="drawer-head">
        <div>
          <h2 style="font-size:18px;font-weight:700;">
            {{ detail ? detail.route.full_domain : t('common.loading') }}
          </h2>
          <p v-if="detail" style="font-size:14px;color:var(--text-secondary);margin-top:2px;">
            {{ detail.route.edge_node_name }} ({{ detail.route.edge_node_public_ip }}) | {{ detail.route.preset.toUpperCase() }}
          </p>
        </div>
        <button class="icon-btn" type="button" @click="emit('close')" :aria-label="t('common.close')">✕</button>
      </div>

      <div v-if="loading && !detail" class="drawer-body">
        <p style="color:var(--text-secondary);">{{ t('common.loading') }}</p>
      </div>

      <div v-else-if="detail" class="drawer-body">
        <div v-if="errMsg" class="alert-banner">{{ errMsg }}</div>

        <div v-if="detail.route.last_error" class="alert-banner">
          {{ detail.route.last_error }}
        </div>

        <!-- Dynamic Link Path Breakdown -->
        <div class="field">
          <span class="field-label">{{ t('inspect.path') }}</span>
          <div class="step-list">
            <div class="step-row" :class="detail.route.local_healthy ? 'done' : 'error'">
              <span>{{ t('inspect.local') }}</span>
              <span class="tnum">{{ detail.route.local_target_host }}:{{ detail.route.local_target_port }} ({{ detail.route.local_healthy ? t('common.ok') : t('inspect.unreachable') }})</span>
            </div>
            <div v-if="detail.route.forward_mode === 'relay'" class="step-row done">
              <span>{{ t('inspect.relay') }}</span>
              <span class="tnum">{{ detail.route.effective_dial }}</span>
            </div>
            <div class="step-row" :class="detail.route.caddy_synced ? 'done' : 'error'">
              <span>{{ t('inspect.edge', { name: detail.route.edge_node_name }) }}</span>
              <span class="tnum">
                {{ detail.route.caddy_synced ? t('inspect.synced') : t('inspect.notSynced') }}{{ detail.route.edge_node_ts_rtt_ms >= 0 ? ` (${detail.route.edge_node_ts_rtt_ms}ms)` : '' }}
              </span>
            </div>
            <div v-if="detail.route.dns_proxied" class="step-row done">
              <span>Cloudflare CDN</span>
              <span>{{ t('inspect.cdnOn') }}</span>
            </div>
            <div class="step-row" :class="detail.route.public_http_code > 0 && detail.route.public_http_code < 500 ? 'done' : 'running'">
              <span>{{ t('inspect.public') }}</span>
              <span class="tnum">
                {{ detail.route.public_http_code > 0 ? `HTTP ${detail.route.public_http_code} (${detail.route.public_latency_ms}ms)` : t('inspect.pending') }}
              </span>
            </div>
          </div>
        </div>

        <!-- Service Configuration -->
        <div class="field">
          <div style="display:flex;justify-content:space-between;align-items:center;gap:8px;">
            <span class="field-label">{{ t('edit.title') }}</span>
            <button v-if="!editing" type="button" class="btn-outline btn-sm" :disabled="actionBusy || applying" @click="startEdit">
              {{ t('edit.edit') }}
            </button>
          </div>

          <div v-if="applying" class="republish-note" style="justify-content:flex-start;">
            <span class="spinner" aria-hidden="true"></span>{{ t('edit.applying') }}
          </div>

          <div v-if="!editing" class="kv-list">
            <div class="kv-row">
              <span>{{ t('edit.target') }}</span>
              <span class="tnum">
                {{ detail.route.source_type === 'self' ? t('edit.selfTarget') : detail.route.source_type === 'docker' ? `${detail.route.container_name} → ` : '' }}{{ detail.route.source_type === 'self' ? '' : `${detail.route.upstream_scheme}://${detail.route.local_target_host}:${detail.route.local_target_port}` }}
              </span>
            </div>
            <div class="kv-row">
              <span>{{ t('edit.preset') }} / {{ t('edit.mode') }}</span>
              <span>{{ presetOptions.find((p) => p.id === detail!.route.preset)?.name }} · {{ detail.route.forward_mode.toUpperCase() }}</span>
            </div>
            <div class="kv-row">
              <span>{{ t('edit.advanced') }}</span>
              <span class="chip-list">
                <span v-for="c in advancedChips" :key="c" class="badge">{{ c }}</span>
              </span>
            </div>
            <div v-if="detail.route.note" class="kv-row">
              <span>{{ t('edit.note') }}</span>
              <span>{{ detail.route.note }}</span>
            </div>
          </div>

          <form v-else class="edit-form" @submit.prevent="saveEdit">
            <p class="adv-hint">{{ t('edit.domainFixed') }}</p>

            <div v-if="detail.route.source_type !== 'self'" class="adv-field">
              <span class="adv-label">{{ t('edit.target') }}</span>
              <div class="segmented" style="align-self:flex-start;">
                <button type="button" class="seg-btn" :class="{ active: edit.source === 'docker' }" :disabled="containerOptions.length === 0" @click="edit.source = 'docker'">
                  {{ t('publish.tabDocker', { n: containers.length }) }}
                </button>
                <button type="button" class="seg-btn" :class="{ active: edit.source === 'manual' }" @click="edit.source = 'manual'">
                  {{ t('publish.tabManual') }}
                </button>
              </div>
              <div v-if="edit.source === 'docker'" class="edit-target-row">
                <select v-model="edit.container" class="select" :aria-label="t('edit.target')">
                  <option v-for="name in containerOptions" :key="name" :value="name">{{ name }}</option>
                </select>
                <input v-model.number="edit.containerPort" class="input tnum" type="number" min="1" max="65535" :placeholder="t('edit.containerPort')" />
              </div>
              <div v-else class="edit-target-row manual">
                <select v-model="edit.scheme" class="select" :aria-label="t('publish.scheme')">
                  <option value="http">http://</option>
                  <option value="https">https://</option>
                </select>
                <input v-model="edit.host" class="input tnum" type="text" required :placeholder="t('publish.hostPlaceholder')" />
                <input v-model.number="edit.port" class="input tnum" type="number" min="1" max="65535" required placeholder="8080" />
              </div>
            </div>

            <div class="adv-grid">
              <label class="adv-field">
                <span class="adv-label">{{ t('edit.node') }}</span>
                <select v-model="edit.node" class="select">
                  <option v-for="n in nodes" :key="n.id" :value="n.id">{{ n.name }} ({{ n.public_ipv4 }})</option>
                </select>
              </label>
              <label class="adv-field">
                <span class="adv-label">{{ t('edit.mode') }}</span>
                <select v-model="edit.mode" class="select">
                  <option value="auto">{{ t('publish.modeAuto') }}</option>
                  <option value="direct">{{ t('publish.modeDirect') }}</option>
                  <option value="relay">{{ t('publish.modeRelay') }}</option>
                </select>
              </label>
              <label class="adv-field">
                <span class="adv-label">{{ t('edit.preset') }}</span>
                <select v-model="edit.preset" class="select">
                  <option v-for="p in presetOptions" :key="p.id" :value="p.id">{{ p.name }}</option>
                </select>
              </label>
              <label class="adv-field">
                <span class="adv-label">{{ t('edit.note') }}</span>
                <input v-model="edit.note" class="input" type="text" :placeholder="t('edit.notePh')" />
              </label>
            </div>

            <span class="adv-label" style="margin-top:4px;">{{ t('adv.title') }}</span>
            <ProxyAdvancedFields v-model="editAdvanced" :preset="edit.preset" :dns-proxied="detail.route.dns_proxied" />

            <div style="display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap;">
              <label style="display:inline-flex;align-items:center;gap:7px;font-size:14px;color:var(--text-secondary);cursor:pointer;">
                <input v-model="edit.skipCheck" type="checkbox" />
                {{ t('publish.skipCheck') }}
              </label>
              <span style="display:flex;gap:8px;">
                <button type="button" class="btn-outline btn-sm" @click="editing = false">{{ t('common.cancel') }}</button>
                <button type="submit" class="btn-primary btn-sm">{{ t('edit.save') }}</button>
              </span>
            </div>
          </form>
        </div>

        <!-- TLS Certificate Info (Single card when DNS Only; Dual card ONLY when CDN proxy is active) -->
        <div class="field">
          <span class="field-label">{{ t('inspect.tls') }}</span>
          <div
            v-if="detail.route.dns_proxied"
            style="display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;"
          >
            <div style="padding:12px 14px;border-radius:8px;border:1px solid var(--border-line);background:var(--bg-subtle);">
              <div style="font-size:12px;color:var(--text-muted);">{{ t('inspect.originCert') }}</div>
              <div style="font-size:15px;font-weight:700;margin-top:4px;">
                {{ detail.route.origin_cert_issuer || t('inspect.issuing') }}
              </div>
              <div class="tnum" style="font-size:14px;margin-top:4px;" :style="{ color: detail.route.origin_cert_days >= 14 ? 'var(--ok-fg)' : 'var(--warn-fg)' }">
                {{ detail.route.origin_cert_days >= 0 ? t('inspect.daysLeft', { n: detail.route.origin_cert_days }) : t('inspect.toIssue') }}
              </div>
            </div>

            <div style="padding:12px 14px;border-radius:8px;border:1px solid var(--border-line);background:var(--bg-subtle);">
              <div style="font-size:12px;color:var(--text-muted);">{{ t('inspect.cdnCert') }}</div>
              <div style="font-size:15px;font-weight:700;margin-top:4px;">
                {{ detail.route.public_cert_issuer || 'Cloudflare' }}
              </div>
              <div style="font-size:14px;color:var(--text-secondary);margin-top:4px;">
                {{ t('inspect.visitorSide') }}
              </div>
            </div>
          </div>

          <div
            v-else
            style="padding:12px 14px;border-radius:8px;border:1px solid var(--border-line);background:var(--bg-subtle);display:flex;align-items:center;justify-content:space-between;gap:12px;"
          >
            <div>
              <div style="font-size:12px;color:var(--text-muted);">{{ t('inspect.issuer') }}</div>
              <div style="font-size:15px;font-weight:700;margin-top:2px;">
                {{ detail.route.origin_cert_issuer || t('inspect.issuing') }}
              </div>
            </div>
            <div class="tnum" style="font-size:14px;font-weight:600;" :style="{ color: detail.route.origin_cert_days >= 14 ? 'var(--ok-fg)' : 'var(--text-secondary)' }">
              {{ detail.route.origin_cert_days >= 0 ? t('inspect.daysLeft', { n: detail.route.origin_cert_days }) : t('inspect.toIssue') }}
            </div>
          </div>
        </div>

        <!-- Access Authentication -->
        <div class="field">
          <span class="field-label">{{ t('route.authTitle') }}</span>
          <div class="auth-panel">
            <div v-if="!authEditing" class="auth-panel-row">
              <span style="display:inline-flex;align-items:center;gap:8px;font-size:14px;">
                <span class="badge" :class="detail.route.basic_auth_user ? 'ok' : ''">
                  {{ detail.route.basic_auth_user ? t('common.on') : t('common.off') }}
                </span>
                <span v-if="detail.route.basic_auth_user" class="tnum">{{ t('route.authUserIs', { user: detail.route.basic_auth_user }) }}</span>
                <span v-else style="color:var(--text-secondary);">{{ t('route.authHint') }}</span>
              </span>
              <span style="display:flex;gap:8px;">
                <button type="button" class="btn-outline btn-sm" :disabled="actionBusy" @click="startAuthEdit">
                  {{ detail.route.basic_auth_user ? t('route.authChange') : t('route.authEnable') }}
                </button>
                <button v-if="detail.route.basic_auth_user" type="button" class="btn-outline btn-sm" :disabled="actionBusy" @click="applyAuth('', '')">
                  {{ t('route.authDisable') }}
                </button>
              </span>
            </div>
            <form v-else class="auth-grid" @submit.prevent="saveAuth">
              <input v-model="authUser" class="input" type="text" autocomplete="off" :placeholder="t('route.authUser')" :aria-label="t('route.authUser')" />
              <input v-model="authPassword" class="input" type="password" autocomplete="new-password" :placeholder="t('route.authPassword')" :aria-label="t('route.authPassword')" />
              <span style="display:flex;gap:8px;grid-column:1 / -1;">
                <button type="submit" class="btn-primary btn-sm" :disabled="actionBusy">{{ t('dnsModal.save') }}</button>
                <button type="button" class="btn-outline btn-sm" @click="authEditing = false">{{ t('common.cancel') }}</button>
              </span>
            </form>
          </div>
        </div>

        <!-- Uptime History Bars -->
        <div class="field">
          <div style="display:flex;justify-content:space-between;align-items:center;">
            <span class="field-label">{{ t('inspect.history') }}</span>
            <span class="tnum" style="font-size:12px;color:var(--text-secondary);">{{ t('inspect.historyCount', { n: detail.history.length }) }}</span>
          </div>
          <div class="uptime-bars">
            <div
              v-for="bar in paddedHistory"
              :key="bar.id"
              class="uptime-bar"
              :class="{
                empty: bar.empty,
                err: !bar.empty && !bar.is_ok,
                warn: !bar.empty && bar.is_ok && bar.public_latency_ms > 500,
              }"
              :title="bar.empty ? t('inspect.noData') : bar.is_ok ? t('inspect.barOK', { ms: bar.public_latency_ms }) : t('inspect.barErr', { stage: bar.fail_stage })"
            ></div>
          </div>
        </div>

        <!-- Rendered Caddy JSON -->
        <div class="field">
          <span class="field-label">{{ t('inspect.caddy') }}</span>
          <pre class="code-block" style="max-height:200px;overflow-y:auto;">{{ JSON.stringify(detail.caddy_payload, null, 2) }}</pre>
        </div>

        <!-- Confirm Delete Box -->
        <div
          v-if="confirmDelete"
          style="padding:14px;border-radius:8px;border:1px solid var(--err-border);background:var(--err-bg);display:flex;flex-direction:column;gap:10px;"
        >
          <strong style="color:var(--err-fg);font-size:14px;">{{ t('inspect.confirmDelete', { domain: detail.route.full_domain }) }}</strong>
          <label style="display:inline-flex;align-items:center;gap:8px;font-size:14px;color:var(--text-primary);cursor:pointer;">
            <input v-model="deleteDNS" type="checkbox" />
            {{ t('inspect.deleteDNS') }}
          </label>
          <div style="display:flex;gap:8px;">
            <button type="button" class="btn-danger" @click="executeDelete">{{ t('inspect.doDelete') }}</button>
            <button type="button" class="btn-outline" @click="confirmDelete = false">{{ t('common.cancel') }}</button>
          </div>
        </div>
      </div>

      <div v-if="detail" class="drawer-foot">
        <div style="display:flex;gap:8px;flex-wrap:wrap;">
          <button type="button" class="btn-outline" :disabled="actionBusy" @click="runProbe">
            {{ t('inspect.probe') }}
          </button>
          <button type="button" class="btn-outline" :disabled="actionBusy" @click="runRetry">
            {{ t('inspect.retry') }}
          </button>
          <button type="button" class="btn-outline" :disabled="actionBusy" @click="toggleEnabled">
            {{ detail.route.enabled ? t('inspect.pause') : t('inspect.resume') }}
          </button>
        </div>
        <button type="button" class="btn-danger" :disabled="actionBusy" @click="confirmDelete = true">
          {{ t('common.delete') }}
        </button>
      </div>
    </aside>
  </div>
</template>
