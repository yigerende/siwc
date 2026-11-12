<template>
  <div v-if="error" class="text-xs text-gray-400">{{ t('admin.accounts.recentRequests.unavailable') }}</div>
  <div v-else-if="errorOnly" class="w-56 min-w-0 max-w-full text-xs">
    <div v-if="latestError" :title="describe(latestError)" class="flex items-center gap-1 text-red-600 dark:text-red-400">
      <span class="shrink-0 font-mono">{{ statusLabel(latestError) }}</span>
      <span class="truncate">{{ latestError.error_message || t('admin.accounts.recentRequests.failed') }}</span>
    </div>
    <span v-else class="text-gray-400">{{ loading ? t('common.loading') : '-' }}</span>
  </div>
  <div v-else class="w-28" :aria-busy="loading">
    <div class="mb-1 h-4 whitespace-nowrap text-[11px] text-gray-500 dark:text-gray-400" :title="newest ? formatDateTime(newest.created_at) : ''">
      {{ newest ? formatDateTime(newest.created_at).slice(5) : loading ? t('common.loading') : '-' }}
    </div>
    <div class="flex h-4 items-center gap-1" :aria-label="t('admin.accounts.columns.recentRequests')">
      <span
        v-for="(request, index) in slots" :key="index"
        class="h-3 w-1.5 shrink-0 rounded-sm"
        :class="!request ? 'bg-gray-200 dark:bg-dark-600' : request.failed ? 'bg-red-500' : 'bg-emerald-500'"
        :data-request-status="!request ? 'empty' : request.failed ? 'failed' : 'success'"
        :title="request ? describe(request) : t('admin.accounts.recentRequests.empty')"
        role="img"
        :aria-label="request ? describe(request) : t('admin.accounts.recentRequests.empty')"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { AccountRecentRequest } from '@/api/admin/accounts'
import { formatDateTime } from '@/utils/format'

const props = withDefaults(defineProps<{
  requests?: AccountRecentRequest[]
  loading?: boolean
  error?: boolean
  errorOnly?: boolean
}>(), { requests: () => [], loading: false, error: false, errorOnly: false })
const { t } = useI18n()
const recent = computed(() => props.requests.slice(0, 10))
const newest = computed(() => recent.value[0])
const latestError = computed(() => recent.value.find(item => item.failed || item.error_message))
const slots = computed(() => [
  ...Array<null>(10 - recent.value.length).fill(null),
  ...recent.value.slice().reverse()
])
const statusLabel = (item: AccountRecentRequest) => item.status_code && item.status_code >= 400
  ? `HTTP ${item.status_code}` : t('admin.accounts.recentRequests.error')
const describe = (item: AccountRecentRequest) => [
  formatDateTime(item.created_at),
  t(item.failed ? 'admin.accounts.recentRequests.failed' : 'admin.accounts.recentRequests.success'),
  item.error_message ? `${statusLabel(item)}: ${item.error_message}` : '',
  !item.failed && item.error_message ? t('admin.accounts.recentRequests.recovered') : ''
].filter(Boolean).join(' | ')
</script>
