<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { api, APIRequestError, type RouteDetail } from '../api'

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
    emit('updated', `已刷新 ${detail.value.route.full_domain} 状态`)
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
    emit('updated', `已同步 ${detail.value.route.full_domain} 配置`)
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
    if (detail.value.route.enabled) {
      await api.pauseRoute(detail.value.route.id)
      emit('updated', `已暂停 ${detail.value.route.full_domain}`)
    } else {
      await api.resumeRoute(detail.value.route.id)
      emit('updated', `已恢复 ${detail.value.route.full_domain}`)
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
    emit('updated', `已删除 ${dom}`)
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
    <aside class="drawer" role="dialog" aria-modal="true" aria-label="路由详情">
      <div class="drawer-head">
        <div>
          <h2 style="font-size:18px;font-weight:700;">
            {{ detail ? detail.route.full_domain : '加载中...' }}
          </h2>
          <p v-if="detail" style="font-size:14px;color:var(--text-secondary);margin-top:2px;">
            {{ detail.route.edge_node_name }} ({{ detail.route.edge_node_public_ip }}) | {{ detail.route.preset.toUpperCase() }}
          </p>
        </div>
        <button class="icon-btn" type="button" @click="emit('close')" aria-label="关闭">✕</button>
      </div>

      <div v-if="loading" class="drawer-body">
        <p style="color:var(--text-secondary);">加载中...</p>
      </div>

      <div v-else-if="detail" class="drawer-body">
        <div v-if="errMsg" class="alert-banner">{{ errMsg }}</div>

        <div v-if="detail.route.last_error" class="alert-banner">
          {{ detail.route.last_error }}
        </div>

        <!-- Dynamic Link Path Breakdown -->
        <div class="field">
          <span class="field-label">链路状态</span>
          <div class="step-list">
            <div class="step-row" :class="detail.route.local_healthy ? 'done' : 'error'">
              <span>本地目标</span>
              <span class="tnum">{{ detail.route.local_target_host }}:{{ detail.route.local_target_port }} ({{ detail.route.local_healthy ? '正常' : '不可达' }})</span>
            </div>
            <div v-if="detail.route.forward_mode === 'relay'" class="step-row done">
              <span>本机中继 (Local Relay)</span>
              <span class="tnum">{{ detail.route.effective_dial }}</span>
            </div>
            <div class="step-row" :class="detail.route.caddy_synced ? 'done' : 'error'">
              <span>边缘节点 ({{ detail.route.edge_node_name }})</span>
              <span class="tnum">
                {{ detail.route.caddy_synced ? '配置已同步' : '未同步' }}{{ detail.route.edge_node_ts_rtt_ms >= 0 ? ` (${detail.route.edge_node_ts_rtt_ms}ms)` : '' }}
              </span>
            </div>
            <div v-if="detail.route.dns_proxied" class="step-row done">
              <span>Cloudflare CDN</span>
              <span>已开启代理</span>
            </div>
            <div class="step-row" :class="detail.route.public_http_code > 0 && detail.route.public_http_code < 500 ? 'done' : 'running'">
              <span>公网 HTTPS 响应</span>
              <span class="tnum">
                {{ detail.route.public_http_code > 0 ? `HTTP ${detail.route.public_http_code} (${detail.route.public_latency_ms}ms)` : '待检测' }}
              </span>
            </div>
          </div>
        </div>

        <!-- TLS Certificate Info (Single card when DNS Only; Dual card ONLY when CDN proxy is active) -->
        <div class="field">
          <span class="field-label">TLS 证书</span>
          <div
            v-if="detail.route.dns_proxied"
            style="display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:10px;"
          >
            <div style="padding:12px 14px;border-radius:8px;border:1px solid var(--border-line);background:var(--bg-subtle);">
              <div style="font-size:12px;color:var(--text-muted);">边缘源站证书</div>
              <div style="font-size:15px;font-weight:700;margin-top:4px;">
                {{ detail.route.origin_cert_issuer || '签发中' }}
              </div>
              <div class="tnum" style="font-size:14px;margin-top:4px;" :style="{ color: detail.route.origin_cert_days >= 14 ? 'var(--ok-fg)' : 'var(--warn-fg)' }">
                {{ detail.route.origin_cert_days >= 0 ? `剩余 ${detail.route.origin_cert_days} 天` : '待签发' }}
              </div>
            </div>

            <div style="padding:12px 14px;border-radius:8px;border:1px solid var(--border-line);background:var(--bg-subtle);">
              <div style="font-size:12px;color:var(--text-muted);">CDN 边缘证书</div>
              <div style="font-size:15px;font-weight:700;margin-top:4px;">
                {{ detail.route.public_cert_issuer || 'Cloudflare' }}
              </div>
              <div style="font-size:14px;color:var(--text-secondary);margin-top:4px;">
                访客侧生效
              </div>
            </div>
          </div>

          <div
            v-else
            style="padding:12px 14px;border-radius:8px;border:1px solid var(--border-line);background:var(--bg-subtle);display:flex;align-items:center;justify-content:space-between;gap:12px;"
          >
            <div>
              <div style="font-size:12px;color:var(--text-muted);">签发机构</div>
              <div style="font-size:15px;font-weight:700;margin-top:2px;">
                {{ detail.route.origin_cert_issuer || '签发中' }}
              </div>
            </div>
            <div class="tnum" style="font-size:14px;font-weight:600;" :style="{ color: detail.route.origin_cert_days >= 14 ? 'var(--ok-fg)' : 'var(--text-secondary)' }">
              {{ detail.route.origin_cert_days >= 0 ? `剩余 ${detail.route.origin_cert_days} 天` : '待签发' }}
            </div>
          </div>
        </div>

        <!-- Uptime History Bars -->
        <div class="field">
          <div style="display:flex;justify-content:space-between;align-items:center;">
            <span class="field-label">近期检测记录</span>
            <span class="tnum" style="font-size:12px;color:var(--text-secondary);">{{ detail.history.length }} 次</span>
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
              :title="bar.empty ? '无数据' : bar.is_ok ? `正常 (${bar.public_latency_ms}ms)` : `异常: ${bar.fail_stage}`"
            ></div>
          </div>
        </div>

        <!-- Rendered Caddy JSON -->
        <div class="field">
          <span class="field-label">Caddy 路由配置</span>
          <pre class="code-block" style="max-height:200px;overflow-y:auto;">{{ JSON.stringify(detail.caddy_payload, null, 2) }}</pre>
        </div>

        <!-- Confirm Delete Box -->
        <div
          v-if="confirmDelete"
          style="padding:14px;border-radius:8px;border:1px solid var(--err-border);background:var(--err-bg);display:flex;flex-direction:column;gap:10px;"
        >
          <strong style="color:var(--err-fg);font-size:14px;">确认删除 {{ detail.route.full_domain }}？</strong>
          <label style="display:inline-flex;align-items:center;gap:8px;font-size:14px;color:var(--text-primary);cursor:pointer;">
            <input v-model="deleteDNS" type="checkbox" />
            同时删除远端 DNS 解析记录
          </label>
          <div style="display:flex;gap:8px;">
            <button type="button" class="btn-danger" :disabled="actionBusy" @click="executeDelete">确认删除</button>
            <button type="button" class="btn-outline" @click="confirmDelete = false">取消</button>
          </div>
        </div>
      </div>

      <div v-if="detail" class="drawer-foot">
        <div style="display:flex;gap:8px;flex-wrap:wrap;">
          <button type="button" class="btn-outline" :disabled="actionBusy" @click="runProbe">
            立即检测
          </button>
          <button type="button" class="btn-outline" :disabled="actionBusy" @click="runRetry">
            重新同步
          </button>
          <button type="button" class="btn-outline" :disabled="actionBusy" @click="toggleEnabled">
            {{ detail.route.enabled ? '暂停' : '启用' }}
          </button>
        </div>
        <button type="button" class="btn-danger" :disabled="actionBusy" @click="confirmDelete = true">
          删除
        </button>
      </div>
    </aside>
  </div>
</template>
