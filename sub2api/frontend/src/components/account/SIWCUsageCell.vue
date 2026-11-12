<script setup lang="ts">
import { computed } from 'vue'
import type { AccountUsageInfo } from '@/types'
import UsageProgressBar from './UsageProgressBar.vue'

const props = defineProps<{ usage: AccountUsageInfo | null; loading?: boolean; error?: string | null }>()
defineEmits<{ refresh: [] }>()
const checkedAt = computed(() => {
  const date = new Date(props.usage?.updated_at || '')
  return date.getFullYear() > 1970 ? date.toLocaleString() : ''
})
const queryError = computed(() => props.error || props.usage?.error)
const estimatedTotal = computed(() => {
  const window = props.usage?.seven_day
  const used = window?.utilization
  const cost = window?.window_stats?.cost
  if (typeof used !== 'number' || typeof cost !== 'number' || !Number.isFinite(used) ||
      !Number.isFinite(cost) || used <= 0 || cost <= 0 ||
      !window?.resets_at || new Date(window.resets_at).getTime() <= Date.now()) return null
  const estimate = cost * 100 / used
  return Number.isFinite(estimate) ? estimate : null
})
</script>

<template>
  <div class="space-y-1">
    <UsageProgressBar v-if="usage?.five_hour" label="5h"
      :utilization="usage.five_hour.utilization" :resets-at="usage.five_hour.resets_at"
      :window-stats="usage.five_hour.window_stats" :show-empty-stats="true" color="indigo" />
    <div v-else class="text-[10px] text-gray-400">5h：{{ loading && !usage ? '加载中…' : usage?.seven_day ? '未提供' : '待查询' }}</div>
    <UsageProgressBar v-if="usage?.seven_day" label="7d"
      :utilization="usage.seven_day.utilization" :resets-at="usage.seven_day.resets_at"
      :window-stats="usage.seven_day.window_stats" :estimated-total-cost="estimatedTotal"
      :show-empty-stats="true" :show-unavailable-estimate="true" color="emerald" />
    <div v-else class="text-[10px] text-gray-400">7d：{{ loading && !usage ? '加载中…' : usage?.five_hour ? '未提供' : '待查询' }}</div>
    <div class="flex flex-wrap items-center gap-1 text-[10px]">
      <button type="button" class="rounded px-1.5 py-0.5 font-medium text-blue-600 hover:bg-blue-50 disabled:opacity-50 dark:text-blue-400"
        :disabled="loading" @click="$emit('refresh')">{{ loading ? '查询中…' : '刷新' }}</button>
      <span v-if="checkedAt" class="text-gray-400">{{ checkedAt }}</span>
    </div>
    <details v-if="queryError" class="max-w-64 whitespace-normal break-words text-[10px] text-amber-600">
      <summary>{{ usage?.five_hour || usage?.seven_day ? '查询失败或已过期，显示上次数据' : '额度查询失败' }}</summary>
      {{ queryError }}
    </details>
  </div>
</template>
