<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import type { DiscoveredContainer, EdgeNode, RouteRecord } from '../api'
import { t } from '../i18n'

const props = defineProps<{
  routes: RouteRecord[]
  containers: DiscoveredContainer[]
  nodes: EdgeNode[]
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'inspect-route', id: string): void
  (e: 'publish-container', c: DiscoveredContainer): void
  (e: 'open-publish'): void
  (e: 'switch-view', view: 'dashboard' | 'nodes' | 'dns' | 'cli'): void
  (e: 'toggle-theme'): void
  (e: 'toggle-lang'): void
}>()

const query = ref('')
const inputEl = ref<HTMLInputElement | null>(null)

onMounted(() => {
  nextTick(() => inputEl.value?.focus())
})

interface CmdAction {
  id: string
  label: string
  tag: string
  run: () => void
}

const items = computed<CmdAction[]>(() => {
  const list: CmdAction[] = [
    {
      id: 'act-publish',
      label: t('cmd.publish'),
      tag: t('cmd.tagAction'),
      run: () => emit('open-publish'),
    },
    {
      id: 'act-theme',
      label: t('cmd.theme'),
      tag: t('cmd.tagPref'),
      run: () => emit('toggle-theme'),
    },
    {
      id: 'act-lang',
      label: t('cmd.lang'),
      tag: t('cmd.tagPref'),
      run: () => emit('toggle-lang'),
    },
    {
      id: 'nav-dash',
      label: t('cmd.routes'),
      tag: t('cmd.tagNav'),
      run: () => emit('switch-view', 'dashboard'),
    },
    {
      id: 'nav-nodes',
      label: t('cmd.nodes', { n: props.nodes.length }),
      tag: t('cmd.tagNav'),
      run: () => emit('switch-view', 'nodes'),
    },
    {
      id: 'nav-dns',
      label: t('cmd.dns'),
      tag: t('cmd.tagNav'),
      run: () => emit('switch-view', 'dns'),
    },
    {
      id: 'nav-cli',
      label: t('cmd.settings'),
      tag: t('cmd.tagNav'),
      run: () => emit('switch-view', 'cli'),
    },
  ]

  for (const c of props.containers) {
    if (c.published_domains.length === 0) {
      list.push({
        id: `cnt-${c.id}`,
        label: t('cmd.publishContainer', { name: c.name, target: `${c.recommended_host}:${c.recommended_port}` }),
        tag: c.recommended_mode.toUpperCase(),
        run: () => emit('publish-container', c),
      })
    }
  }

  for (const r of props.routes) {
    list.push({
      id: `rt-${r.id}`,
      label: `${r.full_domain} (${r.local_target_host}:${r.local_target_port})`,
      tag: r.edge_node_name,
      run: () => emit('inspect-route', r.id),
    })
  }

  const q = query.value.trim().toLowerCase()
  if (!q) return list
  return list.filter((it) => it.label.toLowerCase().includes(q) || it.tag.toLowerCase().includes(q))
})

function trigger(item: CmdAction) {
  emit('close')
  item.run()
}
</script>

<template>
  <div class="backdrop" @click.self="emit('close')">
    <div class="cmd-modal" role="dialog" aria-modal="true" :aria-label="t('nav.quickSearch')">
      <input
        ref="inputEl"
        v-model="query"
        type="text"
        class="cmd-input"
        :placeholder="t('cmd.placeholder')"
        @keydown.esc="emit('close')"
        @keydown.enter.prevent="items[0] && trigger(items[0])"
      />
      <div class="cmd-list">
        <button
          v-for="it in items"
          :key="it.id"
          type="button"
          class="cmd-item"
          @click="trigger(it)"
        >
          <span>{{ it.label }}</span>
          <span class="badge">{{ it.tag }}</span>
        </button>
        <div v-if="items.length === 0" style="padding:16px;text-align:center;color:var(--text-secondary);font-size:14px;">
          {{ t('cmd.empty') }}
        </div>
      </div>
    </div>
  </div>
</template>
