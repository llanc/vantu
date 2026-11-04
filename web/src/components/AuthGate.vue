<script setup lang="ts">
import { ref } from 'vue'
import { api, APIRequestError } from '../api'
import { t, toggleLocale } from '../i18n'

const props = defineProps<{
  initialized: boolean
  version: string
  theme: 'light' | 'dark'
}>()

const emit = defineEmits<{
  (e: 'authenticated'): void
  (e: 'toggle-theme'): void
}>()

const password = ref('')
const confirmPassword = ref('')
const loading = ref(false)
const errMsg = ref('')

async function handleSubmit() {
  errMsg.value = ''
  if (!props.initialized) {
    if (password.value.length < 6) {
      errMsg.value = t('auth.tooShort')
      return
    }
    if (password.value !== confirmPassword.value) {
      errMsg.value = t('auth.mismatch')
      return
    }
  }

  loading.value = true
  try {
    if (!props.initialized) {
      await api.setupAdmin(password.value)
    } else {
      await api.login(password.value)
    }
    emit('authenticated')
  } catch (err) {
    errMsg.value = err instanceof APIRequestError ? err.message : (err as Error).message
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="auth-shell">
    <div class="auth-card">
      <div style="display:flex;align-items:center;justify-content:space-between;gap:12px;">
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
        </div>
        <div style="display:flex;gap:6px;">
          <button class="icon-btn lang-btn" type="button" :title="t('common.toggleLang')" :aria-label="t('common.toggleLang')" @click="toggleLocale">
            {{ t('common.langShort') }}
          </button>
          <button class="icon-btn" type="button" @click="emit('toggle-theme')" :aria-label="t('common.toggleTheme')">
            <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8">
              <circle cx="12" cy="12" r="4"/>
              <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/>
            </svg>
          </button>
        </div>
      </div>

      <div>
        <h1 style="font-size:20px;font-weight:700;">
          {{ initialized ? t('auth.loginTitle') : t('auth.setupTitle') }}
        </h1>
      </div>

      <div v-if="errMsg" class="alert-banner">{{ errMsg }}</div>

      <form style="display:flex;flex-direction:column;gap:14px;" @submit.prevent="handleSubmit">
        <input type="text" name="username" value="admin" autocomplete="username" hidden />

        <div class="field">
          <label class="field-label" for="authPass">{{ t('auth.password') }}</label>
          <input
            id="authPass"
            v-model="password"
            class="input"
            type="password"
            :autocomplete="initialized ? 'current-password' : 'new-password'"
            :placeholder="initialized ? t('auth.passwordPlaceholder') : t('auth.newPasswordPlaceholder')"
            required
          />
        </div>

        <div v-if="!initialized" class="field">
          <label class="field-label" for="authConfirm">{{ t('auth.confirm') }}</label>
          <input
            id="authConfirm"
            v-model="confirmPassword"
            class="input"
            type="password"
            autocomplete="new-password"
            :placeholder="t('auth.confirmPlaceholder')"
            required
          />
        </div>

        <button class="btn-primary" style="width:100%;margin-top:4px;" type="submit" :disabled="loading">
          {{ loading ? t('auth.submitting') : initialized ? t('auth.login') : t('auth.finish') }}
        </button>
      </form>
    </div>
  </div>
</template>
