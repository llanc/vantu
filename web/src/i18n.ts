import { ref } from 'vue'
import zh from './locales/zh'
import en from './locales/en'

export type Locale = 'zh' | 'en'
export type MessageKey = keyof typeof zh

const STORAGE_KEY = 'vantu_lang'
const dicts: Record<Locale, Record<MessageKey, string>> = { zh, en }

function initialLocale(): Locale {
  return localStorage.getItem(STORAGE_KEY) === 'en' ? 'en' : 'zh'
}

function syncEnvironment(l: Locale) {
  document.documentElement.lang = l === 'en' ? 'en' : 'zh-CN'
  document.title = dicts[l]['common.appName']
  // The backend reads this cookie for requests that cannot carry headers (SSE).
  document.cookie = `${STORAGE_KEY}=${l}; path=/; max-age=31536000; samesite=lax`
}

export const locale = ref<Locale>(initialLocale())
syncEnvironment(locale.value)

export function setLocale(l: Locale) {
  locale.value = l
  localStorage.setItem(STORAGE_KEY, l)
  syncEnvironment(l)
}

export function toggleLocale() {
  setLocale(locale.value === 'zh' ? 'en' : 'zh')
}

export function t(key: MessageKey, params?: Record<string, string | number>): string {
  const s = dicts[locale.value][key] ?? zh[key]
  if (!params) return s
  return s.replace(/\{(\w+)\}/g, (m, k: string) => (k in params ? String(params[k]) : m))
}

export function formatDateTime(iso: string): string {
  return new Date(iso).toLocaleString(locale.value === 'en' ? 'en-US' : 'zh-CN')
}
