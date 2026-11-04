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
import { t } from '../i18n'

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
  (e: 'inspect-domain', domain: string): void
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
const authEnabled = ref(false)
const authUser = ref('')
const authPassword = ref('')

const unpublishedContainers = computed(() => props.containers.filter((c) => c.published_domains.length === 0))
// Published containers stay selectable so a second domain can point at the same service.
const publishedContainers = computed(() => props.containers.filter((c) => c.published_domains.length > 0))
const showPublished = ref(false)

const selectedPublishedDomains = computed(() => {
  if (sourceTab.value !== 'docker') return []
  return props.containers.find((c) => c.name === selectedContainer.value)?.published_domains ?? []
})

const dryRunCLI = ref('')
const loading = ref(false)
const errMsg = ref('')
const conflictWarning = ref(false)

const presets = computed(() => [
  { id: 'web' as const, name: t('publish.presetWeb'), desc: t('publish.presetWebDesc') },
  { id: 'sse' as const, name: t('publish.presetSSE'), desc: t('publish.presetSSEDesc') },
  { id: 'webdav' as const, name: t('publish.presetWebdav'), desc: t('publish.presetWebdavDesc') },
  { id: 'https_insecure' as const, name: t('publish.presetInsecure'), desc: t('publish.presetInsecureDesc') },
])

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
    if (props.initialContainer.published_domains.length > 0) showPublished.value = true
  } else if (unpublishedContainers.value.length > 0) {
    pickContainer(unpublishedContainers.value[0])
  } else if (publishedContainers.value.length > 0) {
    showPublished.value = true
    pickContainer(publishedContainers.value[0])
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
  if (authEnabled.value && authUser.value.trim()) parts.push(`--auth-user ${authUser.value.trim()} --auth-password <password>`)
  return dryRunCLI.value || parts.join(' ')
})

watch([sourceTab, selectedContainer, manualHost, manualPort, manualScheme, subdomain, zoneID, nodeID, preset, forwardMode, dnsProxied, authEnabled, authUser], () => {
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
    basic_auth_user: authEnabled.value ? authUser.value.trim() : undefined,
    basic_auth_password: authEnabled.value ? authPassword.value : undefined,
    skip_local_check: skipLocalCheck.value,
    dry_run: dryRun,
  }
}

// Once the edge has the route, the remaining TLS/probe wait is shown on the route card instead.
let closedEarly = false
watch(
  () => props.liveSteps.find((s) => s.step === 'caddy_push' && s.domain === fullDomainPreview.value)?.status,
  (status) => {
    if (loading.value && status === 'done' && !closedEarly) {
      closedEarly = true
      emit('published', t('publish.configuring', { domain: fullDomainPreview.value }))
    }
  }
)

async function handlePublish() {
  errMsg.value = ''
  conflictWarning.value = false
  if (authEnabled.value && (!authUser.value.trim() || authPassword.value.length < 6)) {
    errMsg.value = t('publish.authInvalid')
    return
  }
  loading.value = true
  try {
    const res = await api.publishRoute(buildPayload(false))
    if (!closedEarly) emit('published', t('publish.done', { domain: res.route.full_domain }))
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
        <h2 id="pubModalTitle" style="font-size:18px;font-weight:700;">{{ t('publish.title') }}</h2>
        <button class="icon-btn" type="button" @click="emit('close')" :aria-label="t('common.close')">✕</button>
      </div>

      <div v-if="zones.length === 0 || nodes.length === 0" class="modal-body">
        <div class="empty-state" style="padding:28px 20px;">
          <h3 style="font-size:17px;font-weight:700;">{{ t('publish.prereqTitle') }}</h3>
          <p style="font-size:14px;color:var(--text-secondary);">
            {{ t('publish.prereqDesc', { zones: zones.length, nodes: nodes.length }) }}
          </p>
          <div style="display:flex;gap:10px;flex-wrap:wrap;margin-top:6px;">
            <button v-if="zones.length === 0" type="button" class="btn-primary" @click="emit('open-dns')">
              {{ t('publish.addDNS') }}
            </button>
            <button v-if="nodes.length === 0" type="button" class="btn-primary" @click="emit('open-node')">
              {{ t('publish.addNode') }}
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
            {{ t('publish.overwriteDNS') }}
          </label>
        </div>

        <!-- 1. Local Target -->
        <div class="field">
          <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;flex-wrap:wrap;">
            <span class="field-label">{{ t('publish.target') }}</span>
            <div class="segmented">
              <button
                type="button"
                class="seg-btn"
                :class="{ active: sourceTab === 'docker' }"
                :disabled="containers.length === 0"
                @click="sourceTab = 'docker'"
              >
                {{ t('publish.tabDocker', { n: containers.length }) }}
              </button>
              <button
                type="button"
                class="seg-btn"
                :class="{ active: sourceTab === 'manual' }"
                @click="sourceTab = 'manual'"
              >
                {{ t('publish.tabManual') }}
              </button>
              <button
                type="button"
                class="seg-btn"
                :class="{ active: sourceTab === 'self' }"
                @click="pickSelf"
              >
                {{ t('publish.tabSelf') }}
              </button>
            </div>
          </div>

          <div
            v-if="sourceTab === 'self'"
            style="padding:10px 14px;border-radius:8px;border:1px solid var(--border-line);background:var(--bg-subtle);font-size:14px;color:var(--text-secondary);"
          >
            {{ t('publish.selfDesc') }}
          </div>

          <template v-if="sourceTab === 'docker' && containers.length > 0">
            <div v-if="unpublishedContainers.length > 0" class="container-pick-list">
              <button
                v-for="c in unpublishedContainers"
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
                <span class="tnum pick-sub">{{ c.recommended_host }}:{{ c.recommended_port }}</span>
              </button>
            </div>
            <div v-else class="field-hint">{{ t('publish.allPublished') }}</div>

            <div v-if="publishedContainers.length > 0" class="published-group">
              <button type="button" class="published-toggle" :aria-expanded="showPublished" @click="showPublished = !showPublished">
                <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" aria-hidden="true" :style="{ transform: showPublished ? 'rotate(90deg)' : 'none', transition: 'transform .15s' }">
                  <path d="m9 6 6 6-6 6"/>
                </svg>
                {{ t('publish.publishedGroup', { count: publishedContainers.length }) }}
              </button>
              <div v-if="showPublished" class="container-pick-list">
                <button
                  v-for="c in publishedContainers"
                  :key="c.id"
                  type="button"
                  class="pick-item published"
                  :class="{ selected: selectedContainer === c.name }"
                  @click="pickContainer(c)"
                >
                  <div style="display:flex;align-items:center;justify-content:space-between;gap:6px;width:100%;">
                    <strong style="font-size:14px;">{{ c.name }}</strong>
                    <span class="badge ok">{{ t('publish.published') }}</span>
                  </div>
                  <span class="tnum pick-sub" :title="c.published_domains.join(', ')">{{ c.published_domains.join(', ') }}</span>
                </button>
              </div>
            </div>
          </template>

          <div v-if="selectedPublishedDomains.length > 0" class="republish-note">
            <span>{{ t('publish.alreadyPublished', { domains: selectedPublishedDomains.join(', ') }) }}</span>
            <button type="button" class="btn-outline btn-sm" @click="emit('inspect-domain', selectedPublishedDomains[0])">
              {{ t('publish.viewExisting') }}
            </button>
          </div>

          <div v-if="sourceTab === 'manual'" style="display:grid;grid-template-columns:110px 1fr 120px;gap:8px;">
            <select v-model="manualScheme" class="select" :aria-label="t('publish.scheme')">
              <option value="http">http://</option>
              <option value="https">https://</option>
            </select>
            <input v-model="manualHost" class="input tnum" type="text" :placeholder="t('publish.hostPlaceholder')" required />
            <input v-model.number="manualPort" class="input tnum" type="number" min="1" max="65535" placeholder="8080" required />
          </div>
        </div>

        <!-- 2. Public Domain & Edge Node -->
        <div class="field">
          <label class="field-label" for="subInput">{{ t('publish.domainNode') }}</label>
          <div class="domain-input-group">
            <input
              id="subInput"
              v-model="subdomain"
              class="input"
              style="flex:1;"
              type="text"
              :placeholder="t('publish.subPlaceholder')"
              required
            />
            <select v-model="zoneID" class="select" :aria-label="t('publish.pickZone')">
              <option v-for="z in zones" :key="z.id" :value="z.id">.{{ z.zone_name }}</option>
            </select>
            <select v-model="nodeID" class="select" :aria-label="t('publish.pickNode')">
              <option v-for="n in nodes" :key="n.id" :value="n.id">
                {{ n.name }} ({{ n.public_ipv4 }})
              </option>
            </select>
          </div>
        </div>

        <!-- 3. Protocol Preset -->
        <div class="field">
          <span class="field-label">{{ t('publish.preset') }}</span>
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
            {{ t('publish.cdn') }}
          </label>
          <div style="display:flex;align-items:center;gap:8px;">
            <span style="font-size:14px;color:var(--text-secondary);">{{ t('publish.mode') }}</span>
            <select v-model="forwardMode" class="select" style="height:34px;" :aria-label="t('publish.mode')">
              <option value="auto">{{ t('publish.modeAuto') }}</option>
              <option value="direct">{{ t('publish.modeDirect') }}</option>
              <option value="relay">{{ t('publish.modeRelay') }}</option>
            </select>
          </div>
        </div>

        <!-- 5. Access Authentication -->
        <div class="field">
          <label style="display:inline-flex;align-items:center;gap:8px;font-size:14px;cursor:pointer;">
            <input v-model="authEnabled" type="checkbox" />
            {{ t('publish.auth') }}
            <span style="font-size:13px;color:var(--text-muted);">{{ t('publish.authDesc') }}</span>
          </label>
          <div v-if="authEnabled" class="auth-grid">
            <input v-model="authUser" class="input" type="text" autocomplete="off" :placeholder="t('route.authUser')" :aria-label="t('route.authUser')" />
            <input v-model="authPassword" class="input" type="password" autocomplete="new-password" :placeholder="t('route.authPassword')" :aria-label="t('route.authPassword')" />
          </div>
        </div>

        <div>
          <button
            type="button"
            style="font-size:13px;color:var(--accent-focus);font-weight:500;padding:0;"
            @click="showCLI = !showCLI"
          >
            {{ showCLI ? t('publish.hideCLI') : t('publish.showCLI') }}
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
            {{ t('publish.skipCheck') }}
          </label>
          <div style="display:flex;gap:10px;">
            <button type="button" class="btn-outline" @click="emit('close')">{{ t('common.cancel') }}</button>
            <button type="submit" class="btn-primary" :disabled="loading">
              {{ loading ? t('publish.submitting') : selectedPublishedDomains.length > 0 ? t('publish.submitAddDomain') : t('publish.submit') }}
            </button>
          </div>
        </div>
      </form>
    </div>
  </div>
</template>
