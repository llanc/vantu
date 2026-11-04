<script setup lang="ts">
import { computed } from 'vue'
import { t } from '../i18n'
import { defaultReadTimeout, type AdvancedForm } from '../proxyForm'

const form = defineModel<AdvancedForm>({ required: true })
const props = defineProps<{
  preset: string
  dnsProxied?: boolean
}>()

const presetSkipsVerify = computed(() => props.preset === 'https_insecure')
</script>

<template>
  <div class="adv-grid">
    <label class="adv-field">
      <span class="adv-label">{{ t('adv.maxBody') }}</span>
      <input v-model.number="form.maxBodyMB" class="input tnum" type="number" min="0" :placeholder="t('adv.unlimited')" />
    </label>
    <label class="adv-field">
      <span class="adv-label">{{ t('adv.readTimeout') }}</span>
      <input
        v-model.number="form.readTimeout"
        class="input tnum"
        type="number"
        min="0"
        :placeholder="t('adv.defaultSeconds', { n: defaultReadTimeout(props.preset) })"
      />
    </label>

    <label class="adv-field adv-wide">
      <span class="adv-label">{{ t('adv.upstreamHost') }}</span>
      <input v-model="form.upstreamHost" class="input tnum" type="text" autocomplete="off" :placeholder="t('adv.upstreamHostPh')" />
      <span class="adv-hint">{{ t('adv.upstreamHostHint') }}</span>
    </label>

    <label class="adv-check">
      <input
        type="checkbox"
        :checked="form.tlsSkipVerify || presetSkipsVerify"
        :disabled="presetSkipsVerify"
        @change="form.tlsSkipVerify = ($event.target as HTMLInputElement).checked"
      />
      <span>
        {{ t('adv.skipVerify') }}
        <span class="adv-hint">{{ presetSkipsVerify ? t('adv.skipVerifyByPreset') : t('adv.skipVerifyHint') }}</span>
      </span>
    </label>
    <label class="adv-check">
      <input v-model="form.compress" type="checkbox" />
      <span>
        {{ t('adv.compress') }}
        <span class="adv-hint">{{ props.preset === 'sse' ? t('adv.compressSSE') : t('adv.compressHint') }}</span>
      </span>
    </label>

    <label class="adv-field adv-wide">
      <span class="adv-label">{{ t('adv.ipAllow') }}</span>
      <textarea v-model="form.ipAllow" class="input adv-textarea tnum" rows="2" :placeholder="t('adv.ipAllowPh')"></textarea>
      <span class="adv-hint" :class="{ 'adv-warn': props.dnsProxied && form.ipAllow.trim() }">{{ t('adv.ipAllowHint') }}</span>
    </label>

    <label class="adv-field">
      <span class="adv-label">{{ t('adv.reqHeaders') }}</span>
      <textarea v-model="form.reqHeaders" class="input adv-textarea tnum" rows="3" placeholder="X-Env: prod"></textarea>
    </label>
    <label class="adv-field">
      <span class="adv-label">{{ t('adv.respHeaders') }}</span>
      <textarea v-model="form.respHeaders" class="input adv-textarea tnum" rows="3" placeholder="X-Frame-Options: DENY"></textarea>
    </label>
    <span class="adv-hint adv-wide">{{ t('adv.headersHint') }}</span>
  </div>
</template>
