<script setup lang="ts">
import { ref } from 'vue'
import { api, APIRequestError } from '../api'

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved', msg: string): void
}>()

const providerType = ref<'cloudflare' | 'dnspod'>('cloudflare')
const name = ref('Cloudflare')
const apiToken = ref('')
const secretId = ref('')
const secretKey = ref('')
const baseUrl = ref('')
const showAdvanced = ref(false)
const loading = ref(false)
const errMsg = ref('')

function switchType(t: 'cloudflare' | 'dnspod') {
  providerType.value = t
  name.value = t === 'cloudflare' ? 'Cloudflare' : 'DNSPod'
  errMsg.value = ''
}

async function submit() {
  errMsg.value = ''
  loading.value = true
  try {
    const res = await api.createDNSProvider({
      name: name.value,
      provider_type: providerType.value,
      api_token: providerType.value === 'cloudflare' ? apiToken.value : undefined,
      secret_id: providerType.value === 'dnspod' ? secretId.value : undefined,
      secret_key: providerType.value === 'dnspod' ? secretKey.value : undefined,
      base_url: baseUrl.value || undefined,
    })
    emit('saved', `已添加 ${res.provider.name}，同步 ${res.zones.length} 个域名`)
  } catch (err) {
    errMsg.value = err instanceof APIRequestError ? err.message : (err as Error).message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="backdrop" @click.self="emit('close')">
    <div class="modal" role="dialog" aria-modal="true" aria-labelledby="dnsModalTitle">
      <div class="modal-head">
        <h2 id="dnsModalTitle" style="font-size:18px;font-weight:700;">添加 DNS 凭据</h2>
        <button class="icon-btn" type="button" @click="emit('close')" aria-label="关闭">✕</button>
      </div>

      <form class="modal-body" @submit.prevent="submit">
        <div v-if="errMsg" class="alert-banner">{{ errMsg }}</div>

        <div class="field">
          <span class="field-label">服务商</span>
          <div class="segmented" style="align-self:flex-start;">
            <button type="button" class="seg-btn" :class="{ active: providerType === 'cloudflare' }" @click="switchType('cloudflare')">
              Cloudflare
            </button>
            <button type="button" class="seg-btn" :class="{ active: providerType === 'dnspod' }" @click="switchType('dnspod')">
              DNSPod
            </button>
          </div>
        </div>

        <div class="field">
          <label class="field-label" for="dnsNameInput">备注名称</label>
          <input id="dnsNameInput" v-model="name" class="input" type="text" required />
        </div>

        <template v-if="providerType === 'cloudflare'">
          <div class="field">
            <label class="field-label" for="cfTokenInput">API Token</label>
            <input
              id="cfTokenInput"
              v-model="apiToken"
              class="input"
              type="password"
              placeholder="需 Zone:Read 与 DNS:Edit 权限"
              required
            />
          </div>
        </template>

        <template v-else>
          <div class="field">
            <label class="field-label" for="dpIdInput">SecretId</label>
            <input id="dpIdInput" v-model="secretId" class="input" type="text" placeholder="AKID..." required />
          </div>
          <div class="field">
            <label class="field-label" for="dpKeyInput">SecretKey</label>
            <input id="dpKeyInput" v-model="secretKey" class="input" type="password" placeholder="SecretKey" required />
          </div>
        </template>

        <div>
          <button
            type="button"
            style="font-size:13px;color:var(--accent-focus);font-weight:500;padding:0;"
            @click="showAdvanced = !showAdvanced"
          >
            {{ showAdvanced ? '收起高级选项' : '高级选项' }}
          </button>
          <div v-if="showAdvanced" class="field" style="margin-top:8px;">
            <label class="field-label" for="dnsBaseUrlInput">自定义 API 地址（选填）</label>
            <input
              id="dnsBaseUrlInput"
              v-model="baseUrl"
              class="input"
              type="text"
              placeholder="留空使用官方默认地址"
            />
          </div>
        </div>

        <div style="display:flex;justify-content:flex-end;gap:10px;padding-top:8px;">
          <button type="button" class="btn-outline" @click="emit('close')">取消</button>
          <button type="submit" class="btn-primary" :disabled="loading">
            {{ loading ? '验证中...' : '保存' }}
          </button>
        </div>
      </form>
    </div>
  </div>
</template>
