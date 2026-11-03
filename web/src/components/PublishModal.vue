<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import {
  api,
  APIRequestError,
  type DiscoveredContainer,
  type DNSZone,
  type EdgeNode,
  type StepProgress,
} from '../api'

const props = defineProps<{
  containers: DiscoveredContainer[]
  zones: DNSZone[]
  nodes: EdgeNode[]
  dockerAvailable: boolean
  initialContainer?: DiscoveredContainer | null
  liveSteps: StepProgress[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'published', msg: string): void
  (e: 'open-dns'): void
  (e: 'open-node'): void
}>()

const sourceTab = ref<'docker' | 'manual' | 'self'>(
  props.dockerAvailable && props.containers.length > 0 ? 'docker' : 'manual'
)
const selectedContainer = ref<string>('')
const manualHost = ref('127.0.0.1')
const manualPort = ref<number>(8080)
const manualScheme = ref<'http' | 'https'>('http')

const subdomain = ref('')
const zoneID = ref('')
const nodeID = ref('')
const preset = ref<'web' | 'sse' | 'webdav' | 'https_insecure'>('web')
const forwardMode = ref<'auto' | 'direct' | 'relay'>('auto')
const dnsProxied = ref(false)
const adoptDNS = ref(false)
const skipLocalCheck = ref(false)
const showCLI = ref(false)

const dryRunCLI = ref('')
const loading = ref(false)
const errMsg = ref('')
const conflictWarning = ref(false)

const presets = [
  { id: 'web' as const, name: '常规 Web', desc: 'HTTP / WebSocket' },
  { id: 'sse' as const, name: 'SSE 流式', desc: '禁用缓冲，适合 AI 接口' },
  { id: 'webdav' as const, name: '大文件传输', desc: '3600s 长连接超时' },
  { id: 'https_insecure' as const, name: 'HTTPS 自签', desc: '跳过内网自签证书校验' },
]

const selectedZoneObj = computed(() => props.zones.find((z) => z.id === zoneID.value) || props.zones[0])
const selectedNodeObj = computed(() => props.nodes.find((n) => n.id === nodeID.value) || props.nodes[0])

const fullDomainPreview = computed(() => {
  const z = selectedZoneObj.value?.zone_name || 'example.com'
  const s = subdomain.value.trim().toLowerCase()
  if (!s || s === '@') return z
  return `${s}.${z}`
})

function pickContainer(c: DiscoveredContainer) {
  sourceTab.value = 'docker'
  selectedContainer.value = c.name
  subdomain.value = c.suggested_sub
  preset.value = c.suggested_preset
}

onMounted(() => {
  const defZone = props.zones.find((z) => z.is_default) || props.zones[0]
  if (defZone) zoneID.value = defZone.id

  const defNode = props.nodes.find((n) => n.is_default) || props.nodes[0]
  if (defNode) nodeID.value = defNode.id

  if (props.initialContainer) {
    pickContainer(props.initialContainer)
  } else if (props.containers.length > 0) {
    pickContainer(props.containers[0])
  } else {
    sourceTab.value = 'manual'
    subdomain.value = 'app'
  }
})

function pickSelf() {
  sourceTab.value = 'self'
  if (!subdomain.value || subdomain.value === 'app') {
    subdomain.value = 'vantu'
  }
  preset.value = 'sse'
}

const computedCLICommand = computed(() => {
  const targetStr =
    sourceTab.value === 'self'
      ? 'self'
      : sourceTab.value === 'docker' && selectedContainer.value
      ? `container:${selectedContainer.value}`
      : `${manualScheme.value === 'https' ? 'https://' : ''}${manualHost.value || '127.0.0.1'}:${manualPort.value || 8080}`
  const parts = ['vantu publish', `--target ${targetStr}`, `--domain ${fullDomainPreview.value}`]
  if (selectedNodeObj.value) parts.push(`--node ${selectedNodeObj.value.name}`)
  if (preset.value !== 'web' && !(sourceTab.value === 'self' && preset.value === 'sse')) {
    parts.push(`--preset ${preset.value}`)
  }
  if (forwardMode.value !== 'auto') parts.push(`--mode ${forwardMode.value}`)
  if (dnsProxied.value && selectedZoneObj.value?.supports_proxy) parts.push('--proxied')
  return dryRunCLI.value || parts.join(' ')
})

watch([sourceTab, selectedContainer, manualHost, manualPort, manualScheme, subdomain, zoneID, nodeID, preset, forwardMode, dnsProxied], () => {
  dryRunCLI.value = ''
})

function buildPayload(dryRun: boolean) {
  const isDocker = sourceTab.value === 'docker'
  const isSelf = sourceTab.value === 'self'
  return {
    target: isSelf ? 'self' : undefined,
    source_type: isSelf ? 'self' : isDocker ? 'docker' : 'manual',
    container_name: isDocker ? selectedContainer.value : undefined,
    upstream_scheme: isSelf ? 'http' : isDocker ? (preset.value === 'https_insecure' ? 'https' : 'http') : manualScheme.value,
    local_host: isDocker || isSelf ? undefined : manualHost.value.trim(),
    local_port: isDocker || isSelf ? undefined : Number(manualPort.value),
    subdomain: subdomain.value.trim() || '@',
    domain: fullDomainPreview.value,
    zone_id: zoneID.value,
    edge_node_id: nodeID.value,
    forward_mode: forwardMode.value,
    preset: preset.value,
    dns_proxied: selectedZoneObj.value?.supports_proxy ? dnsProxied.value : false,
    adopt_dns: adoptDNS.value,
    skip_local_check: skipLocalCheck.value,
    dry_run: dryRun,
  }
}

async function handlePublish() {
  errMsg.value = ''
  conflictWarning.value = false
  loading.value = true
  try {
    const res = await api.publishRoute(buildPayload(false))
    emit('published', `已发布 https://${res.route.full_domain}`)
  } catch (err) {
    if (err instanceof APIRequestError) {
      errMsg.value = `${err.message}${err.suggestion ? ' — ' + err.suggestion : ''}`
      if (err.code === 'DNS_CONFLICT') {
        conflictWarning.value = true
      }
    } else {
      errMsg.value = (err as Error).message
    }
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="backdrop" @click.self="emit('close')">
    <div class="modal" role="dialog" aria-modal="true" aria-labelledby="pubModalTitle">
      <div class="modal-head">
        <h2 id="pubModalTitle" style="font-size:18px;font-weight:700;">发布服务</h2>
        <button class="icon-btn" type="button" @click="emit('close')" aria-label="关闭">✕</button>
      </div>

      <div v-if="zones.length === 0 || nodes.length === 0" class="modal-body">
        <div class="empty-state" style="padding:28px 20px;">
          <h3 style="font-size:17px;font-weight:700;">请先完成前置配置</h3>
          <p style="font-size:14px;color:var(--text-secondary);">
            当前可用域名 {{ zones.length }} 个，边缘节点 {{ nodes.length }} 台。
          </p>
          <div style="display:flex;gap:10px;flex-wrap:wrap;margin-top:6px;">
            <button v-if="zones.length === 0" type="button" class="btn-primary" @click="emit('open-dns')">
              添加 DNS 凭据
            </button>
            <button v-if="nodes.length === 0" type="button" class="btn-primary" @click="emit('open-node')">
              添加边缘节点
            </button>
          </div>
        </div>
      </div>

      <form v-else class="modal-body" @submit.prevent="handlePublish">
        <div v-if="errMsg" class="alert-banner">
          <div>{{ errMsg }}</div>
          <label
            v-if="conflictWarning"
            style="display:inline-flex;align-items:center;gap:8px;margin-top:8px;font-weight:600;cursor:pointer;"
          >
            <input v-model="adoptDNS" type="checkbox" />
            覆盖已有同名 DNS 记录
          </label>
        </div>

        <!-- 1. Local Target -->
        <div class="field">
          <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;flex-wrap:wrap;">
            <span class="field-label">本地目标</span>
            <div class="segmented">
              <button
                type="button"
                class="seg-btn"
                :class="{ active: sourceTab === 'docker' }"
                :disabled="containers.length === 0"
                @click="sourceTab = 'docker'"
              >
                Docker 容器 ({{ containers.length }})
              </button>
              <button
                type="button"
                class="seg-btn"
                :class="{ active: sourceTab === 'manual' }"
                @click="sourceTab = 'manual'"
              >
                IP 与端口
              </button>
              <button
                type="button"
                class="seg-btn"
                :class="{ active: sourceTab === 'self' }"
                @click="pickSelf"
              >
                本机控制台
              </button>
            </div>
          </div>

          <div
            v-if="sourceTab === 'self'"
            style="padding:10px 14px;border-radius:8px;border:1px solid var(--border-line);background:var(--bg-subtle);font-size:14px;color:var(--text-secondary);"
          >
            自动指向当前实例监听端口并启用 SSE 流式转发
          </div>

          <div v-if="sourceTab === 'docker' && containers.length > 0" class="container-pick-list">
            <button
              v-for="c in containers"
              :key="c.id"
              type="button"
              class="pick-item"
              :class="{ selected: selectedContainer === c.name }"
              @click="pickContainer(c)"
            >
              <div style="display:flex;align-items:center;justify-content:space-between;gap:6px;width:100%;">
                <strong style="font-size:14px;">{{ c.name }}</strong>
                <span class="badge" :class="c.recommended_mode === 'direct' ? 'ok' : 'info'">
                  {{ c.recommended_mode === 'direct' ? 'Direct' : 'Relay' }}
                </span>
              </div>
              <span class="tnum" style="font-size:12px;color:var(--text-secondary);">
                {{ c.recommended_host }}:{{ c.recommended_port }}
              </span>
            </button>
          </div>

          <div v-if="sourceTab === 'manual'" style="display:grid;grid-template-columns:110px 1fr 120px;gap:8px;">
            <select v-model="manualScheme" class="select" aria-label="内网协议">
              <option value="http">http://</option>
              <option value="https">https://</option>
            </select>
            <input v-model="manualHost" class="input tnum" type="text" placeholder="127.0.0.1 或局域网 IP" required />
            <input v-model.number="manualPort" class="input tnum" type="number" min="1" max="65535" placeholder="8080" required />
          </div>
        </div>

        <!-- 2. Public Domain & Edge Node -->
        <div class="field">
          <label class="field-label" for="subInput">域名与出口节点</label>
          <div class="domain-input-group">
            <input
              id="subInput"
              v-model="subdomain"
              class="input"
              style="flex:1;"
              type="text"
              placeholder="子域名（根域名填 @）"
              required
            />
            <select v-model="zoneID" class="select" aria-label="选择根域名">
              <option v-for="z in zones" :key="z.id" :value="z.id">.{{ z.zone_name }}</option>
            </select>
            <select v-model="nodeID" class="select" aria-label="选择边缘节点">
              <option v-for="n in nodes" :key="n.id" :value="n.id">
                {{ n.name }} ({{ n.public_ipv4 }})
              </option>
            </select>
          </div>
        </div>

        <!-- 3. Protocol Preset -->
        <div class="field">
          <span class="field-label">代理预设</span>
          <div class="preset-grid">
            <button
              v-for="pr in presets"
              :key="pr.id"
              type="button"
              class="preset-card"
              :class="{ selected: preset === pr.id }"
              @click="preset = pr.id"
            >
              <div class="preset-name">{{ pr.name }}</div>
              <div class="preset-desc">{{ pr.desc }}</div>
            </button>
          </div>
        </div>

        <!-- 4. Options -->
        <div style="display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:12px;">
          <label
            v-if="selectedZoneObj?.supports_proxy"
            style="display:inline-flex;align-items:center;gap:8px;font-size:14px;cursor:pointer;"
          >
            <input v-model="dnsProxied" type="checkbox" />
            开启 Cloudflare CDN 代理
          </label>
          <div style="display:flex;align-items:center;gap:8px;">
            <span style="font-size:14px;color:var(--text-secondary);">转发方式:</span>
            <select v-model="forwardMode" class="select" style="height:34px;" aria-label="转发方式">
              <option value="auto">自动</option>
              <option value="direct">Direct 直连</option>
              <option value="relay">Local Relay 中继</option>
            </select>
          </div>
        </div>

        <div>
          <button
            type="button"
            style="font-size:13px;color:var(--accent-focus);font-weight:500;padding:0;"
            @click="showCLI = !showCLI"
          >
            {{ showCLI ? '隐藏 CLI 命令' : '查看等价 CLI 命令' }}
          </button>
          <pre v-if="showCLI" id="cliPreviewBox" class="code-block" style="margin-top:8px;">{{ computedCLICommand }}</pre>
        </div>

        <!-- Real-Time Stepper -->
        <div v-if="liveSteps.length > 0" class="step-list">
          <div
            v-for="st in liveSteps"
            :key="st.step"
            class="step-row"
            :class="st.status"
          >
            <span>{{ st.title }}</span>
            <span class="tnum" style="font-size:14px;">{{ st.detail }}</span>
          </div>
        </div>

        <div style="display:flex;align-items:center;justify-content:space-between;gap:10px;padding-top:4px;flex-wrap:wrap;">
          <label style="display:inline-flex;align-items:center;gap:7px;font-size:14px;color:var(--text-secondary);cursor:pointer;">
            <input v-model="skipLocalCheck" type="checkbox" />
            跳过本地端口可达检查
          </label>
          <div style="display:flex;gap:10px;">
            <button type="button" class="btn-outline" @click="emit('close')">取消</button>
            <button type="submit" class="btn-primary" :disabled="loading">
              {{ loading ? '发布中...' : '发布' }}
            </button>
          </div>
        </div>
      </form>
    </div>
  </div>
</template>
