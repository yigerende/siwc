import { onUnmounted, ref, watch, type Ref } from 'vue'
import type { AccountRecentRequest, AccountRecentRequests } from '@/api/admin/accounts'

type FetchRecent = (ids: number[], signal?: AbortSignal) => Promise<{ accounts: AccountRecentRequests[] }>

export function useAccountRecentRequests(
  accountIDs: Ref<number[]>,
  enabled: Ref<boolean>,
  fetchRecent: FetchRecent
) {
  const requests = ref<Record<string, AccountRecentRequest[]>>({})
  const loading = ref(false)
  const error = ref(false)
  let controller: AbortController | null = null
  let pending: Promise<void> | null = null

  const invalidate = () => {
    controller?.abort()
    controller = null
    pending = null
    requests.value = {}
    loading.value = false
    error.value = false
  }

  // Cancel obsolete page/filter responses even when the account list changes
  // without the page's explicit load wrapper (sorting/debounced search).
  watch(() => accountIDs.value.join(','), invalidate, { flush: 'sync' })
  watch(enabled, invalidate, { flush: 'sync' })
  onUnmounted(invalidate)

  const refresh = (): Promise<void> => {
    if (!enabled.value || accountIDs.value.length === 0) {
      invalidate()
      return Promise.resolve()
    }
    if (pending) return pending
    const current = new AbortController()
    controller = current
    const ids = [...accountIDs.value]
    loading.value = true
    error.value = false
    pending = (async () => {
      try {
        const next: Record<string, AccountRecentRequest[]> = {}
        // Large configured pages still use bounded, serial batches.
        for (let offset = 0; offset < ids.length; offset += 100) {
          const batch = ids.slice(offset, offset + 100)
          const result = await fetchRecent(batch, current.signal)
          if (controller !== current) return
          const allowed = new Set(batch)
          for (const item of result.accounts) {
            if (allowed.has(item.account_id)) next[String(item.account_id)] = item.requests.slice(0, 10)
          }
        }
        if (controller === current) requests.value = next
      } catch {
        if (controller === current) {
          requests.value = {}
          error.value = true
        }
      } finally {
        if (controller === current) {
          loading.value = false
          pending = null
          controller = null
        }
      }
    })()
    return pending
  }

  return { requests, loading, error, refresh }
}
