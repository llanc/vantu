<script setup lang="ts">
import { ref, watch } from 'vue'
import { api, APIRequestError, type BootstrapBundle } from '../api'

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved', msg: string): void
  (e: 'toast', msg: string): void
}>()

const name = ref('tokyo-vps')
const region = ref('')
const publicIP = ref('')
const tailscaleIP = ref('')
const caddyAdminURL = ref('')
const authSecret = ref('')
const isDefault = ref(true)
const showCustomAdmin = ref(false)

const bundle = ref<BootstrapBundle | null>(null)
const activeSnippet = ref<'quick' | 'compose' | 'json'>('quick')
const loading = ref(false)
const errMsg = ref('')

async function refreshBootstrap() {
  try {
    const res = await api.previewBootstrap({
      name: name.value || 'edge-vps',
      public_ipv4: publicIP.value || '203.0.113.10',
      tailscale_ip: tailscaleIP.value || '100.x.x.x',
      auth_secret: authSecret.value || undefined,
    })
    bundle.value = res
    if (!authSecret.value) {
      authSecret.value = res.auth_secret
    }
  } catch {
    // Ignore preview errors
  }
}

watch([name, publicIP, tailscaleIP], () => {
  refreshBootstrap()
}, { immediate: true })

function copySnippet(text: string) {
  navigator.clipboard?.writeText(text)
  emit('toast', '已复制到剪贴板')
}

async function submit() {
  errMsg.value = ''
  loading.value = true
  try {
    const node = await api.createNode({
      name: name.value,
      region: region.value,
      public_ipv4: publicIP.value,
      tailscale_ip: tailscaleIP.value,
      caddy_admin_url: caddyAdminURL.value || undefined,
      auth_secret: authSecret.value,
      is_default: isDefault.value,
    })
    emit('saved', `已添加节点 ${node.name}`)
  } catch (err) {
    errMsg.value = err instanceof APIRequestError
      ? `${err.message}${err.suggestion ? ' — ' + err.suggestion : ''}`
      : (err as Error).message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="backdrop" @click.self="emit('close')">
    <div class="modal" style="max-width:700px;" role="dialog" aria-modal="true" aria-labelledby="nodeModalTitle">
      <div class="modal-head">
        <h2 id="nodeModalTitle" style="font-size:18px;font-weight:700;">添加边缘节点</h2>
        <button class="icon-btn" type="button" @click="emit('close')" aria-label="关闭">✕</button>
      </div>

      <form class="modal-body" @submit.prevent="submit">
        <div v-if="errMsg" class="alert-banner">{{ errMsg }}</div>

        <div style="display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:12px;">
          <div class="field">
            <label class="field-label" for="nodeNameIn">节点名称</label>
            <input id="nodeNameIn" v-model="name" class="input" type="text" placeholder="tokyo-vps" required />
          </div>
          <div class="field">
            <label class="field-label" for="nodeRegionIn">区域备注（选填）</label>
            <input id="nodeRegionIn" v-model="region" class="input" type="text" placeholder="Tokyo / HK" />
          </div>
          <div class="field">
            <label class="field-label" for="nodePubIpIn">公网 IPv4</label>
            <input id="nodePubIpIn" v-model="publicIP" class="input tnum" type="text" placeholder="45.76.210.18" required />
          </div>
          <div class="field">
            <label class="field-label" for="nodeTsIpIn">Tailscale IP</label>
            <input id="nodeTsIpIn" v-model="tailscaleIP" class="input tnum" type="text" placeholder="100.116.63.43" required />
          </div>
        </div>

        <div class="field">
          <div style="display:flex;align-items:center;justify-content:space-between;gap:8px;">
            <label class="field-label" for="nodeSecIn">Auth Secret</label>
            <button
              type="button"
              style="font-size:13px;color:var(--accent-focus);font-weight:500;"
              @click="showCustomAdmin = !showCustomAdmin"
            >
              {{ showCustomAdmin ? '收起 Admin URL' : '自定义 Admin URL' }}
            </button>
          </div>
          <input id="nodeSecIn" v-model="authSecret" class="input tnum" type="text" required />
        </div>

        <div v-if="showCustomAdmin" class="field">
          <label class="field-label" for="nodeAdminUrlIn">Caddy Admin URL（默认 http://TailscaleIP:2019）</label>
          <input id="nodeAdminUrlIn" v-model="caddyAdminURL" class="input tnum" type="text" placeholder="http://100.116.63.43:2019" />
        </div>

        <div v-if="bundle" class="field">
          <div style="display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:8px;">
            <span class="field-label">远端 Caddy 启动配置</span>
            <div class="segmented">
              <button type="button" class="seg-btn" :class="{ active: activeSnippet === 'quick' }" @click="activeSnippet = 'quick'">一键命令</button>
              <button type="button" class="seg-btn" :class="{ active: activeSnippet === 'compose' }" @click="activeSnippet = 'compose'">docker-compose.yml</button>
              <button type="button" class="seg-btn" :class="{ active: activeSnippet === 'json' }" @click="activeSnippet = 'json'">bootstrap.json</button>
            </div>
          </div>
          <pre class="code-block" style="max-height:180px;overflow-y:auto;">{{
            activeSnippet === 'quick'
              ? bundle.quick_command
              : activeSnippet === 'compose'
              ? bundle.docker_compose
              : bundle.bootstrap_json
          }}</pre>
          <div style="display:flex;align-items:center;justify-content:space-between;gap:10px;flex-wrap:wrap;">
            <label style="display:inline-flex;align-items:center;gap:8px;font-size:14px;color:var(--text-secondary);cursor:pointer;">
              <input v-model="isDefault" type="checkbox" />
              设为默认节点
            </label>
            <button
              type="button"
              class="btn-outline"
              @click="copySnippet(activeSnippet === 'quick' ? bundle.quick_command : activeSnippet === 'compose' ? bundle.docker_compose : bundle.bootstrap_json)"
            >
              复制配置
            </button>
          </div>
        </div>

        <div style="display:flex;justify-content:flex-end;gap:10px;padding-top:4px;">
          <button type="button" class="btn-outline" @click="emit('close')">取消</button>
          <button type="submit" class="btn-primary" :disabled="loading">
            {{ loading ? '连接验证中...' : '测试连接并保存' }}
          </button>
        </div>
      </form>
    </div>
  </div>
</template>
