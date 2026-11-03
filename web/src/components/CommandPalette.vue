<script setup lang="ts">
import { computed, nextTick, onMounted, ref } from 'vue'
import type { DiscoveredContainer, EdgeNode, RouteRecord } from '../api'

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
      label: '发布服务',
      tag: '操作',
      run: () => emit('open-publish'),
    },
    {
      id: 'act-theme',
      label: '切换浅色 / 暗色主题',
      tag: '偏好',
      run: () => emit('toggle-theme'),
    },
    {
      id: 'nav-dash',
      label: '路由列表',
      tag: '导航',
      run: () => emit('switch-view', 'dashboard'),
    },
    {
      id: 'nav-nodes',
      label: `边缘节点 (${props.nodes.length})`,
      tag: '导航',
      run: () => emit('switch-view', 'nodes'),
    },
    {
      id: 'nav-dns',
      label: '域名与 DNS',
      tag: '导航',
      run: () => emit('switch-view', 'dns'),
    },
    {
      id: 'nav-cli',
      label: '设置与 API Token',
      tag: '导航',
      run: () => emit('switch-view', 'cli'),
    },
  ]

  for (const c of props.containers) {
    if (c.published_domains.length === 0) {
      list.push({
        id: `cnt-${c.id}`,
        label: `发布容器 ${c.name} (${c.recommended_host}:${c.recommended_port})`,
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
    <div class="cmd-modal" role="dialog" aria-modal="true" aria-label="快速搜索">
      <input
        ref="inputEl"
        v-model="query"
        type="text"
        class="cmd-input"
        placeholder="搜索域名、容器或页面..."
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
          无匹配结果
        </div>
      </div>
    </div>
  </div>
</template>
