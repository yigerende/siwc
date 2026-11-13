<script setup lang="ts">
import { computed } from 'vue'
import type { Account } from '@/types'

const props = defineProps<{ marker?: Account['space_workflow'] }>()
const labels: Record<string, string> = { merge: '空间合并', merge_v2: '合并2.0', deactivate: '删除账户', merchant: 'API 商户' }
const detail = computed(() => {
  const m = props.marker
  if (!m) return ''
  const at = new Date(m.changed_at)
  const time = Number.isNaN(at.getTime()) ? m.changed_at : at.toLocaleString('zh-CN', { hour12: false })
  return `${labels[m.stage] || m.stage} · ${time}${m.manual ? ' · 手动确认' : ''}\n订单：${m.order_id || m.flow_id}\n表示 Space 最后一步已完成，到账情况以卡台为准。`
})
</script>

<template>
  <details v-if="marker?.completed" class="mt-1 max-w-full self-start text-xs">
    <summary :title="detail" class="cursor-pointer rounded bg-emerald-50 px-1.5 py-0.5 font-medium text-emerald-700 dark:bg-emerald-900/25 dark:text-emerald-300">
      ✓ Space 已完成
    </summary>
    <p class="mt-1 max-w-[240px] whitespace-pre-line break-words text-gray-500 dark:text-gray-400">{{ detail }}</p>
  </details>
</template>
