<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, APIRequestError, type RouteDetail } from '../api'
import { locale, t } from '../i18n'

const props = defineProps<{
  routeId: string | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'updated', msg: string): void
}>()

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

async function executeDelete() {
  if (!detail.value) return
  actionBusy.value = true
  errMsg.value = ''
  try {
    const dom = detail.value.route.full_domain
    await api.deleteRoute(detail.value.route.id, deleteDNS.value)
    emit('updated', t('common.deletedName', { name: dom }))
    emit('close')
  } catch (err) {
    errMsg.value = err instanceof APIRequestError ? err.message : (err as Error).message
  } finally {
    actionBusy.value = false
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
            <button type="button" class="btn-danger" :disabled="actionBusy" @click="executeDelete">{{ t('inspect.doDelete') }}</button>
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
