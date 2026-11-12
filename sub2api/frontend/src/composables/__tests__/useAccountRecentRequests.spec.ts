import { describe, expect, it, vi } from 'vitest'
import { defineComponent, ref } from 'vue'
import { mount } from '@vue/test-utils'
import { useAccountRecentRequests } from '../useAccountRecentRequests'
import type { AccountRecentRequests } from '@/api/admin/accounts'

function setup(fetcher = vi.fn().mockResolvedValue({ accounts: [] })) {
  const ids = ref([1, 2])
  const enabled = ref(true)
  let result!: ReturnType<typeof useAccountRecentRequests>
  const wrapper = mount(defineComponent({ setup() {
    result = useAccountRecentRequests(ids, enabled, fetcher)
    return () => null
  } }))
  return { ids, enabled, result, wrapper, fetcher }
}

describe('current-page account request history', () => {
  it('only fetches visible IDs and skips hidden/empty pages', async () => {
    const state = setup()
    await state.result.refresh()
    expect(state.fetcher).toHaveBeenCalledWith([1, 2], expect.any(AbortSignal))
    state.enabled.value = false
    await state.result.refresh()
    expect(state.fetcher).toHaveBeenCalledTimes(1)
    state.enabled.value = true
    state.ids.value = []
    await state.result.refresh()
    expect(state.fetcher).toHaveBeenCalledTimes(1)
    state.wrapper.unmount()
  })

  it('batches large pages sequentially and coalesces concurrent refreshes', async () => {
    const state = setup()
    state.ids.value = Array.from({ length: 235 }, (_, i) => i + 1)
    const first = state.result.refresh()
    const second = state.result.refresh()
    expect(second).toBe(first)
    await first
    expect(state.fetcher.mock.calls.map(call => call[0].length)).toEqual([100, 100, 35])
    state.wrapper.unmount()
  })

  it('discards old responses and cancels pending work on page changes/unmount', async () => {
    let finish!: (value: { accounts: AccountRecentRequests[] }) => void
    const fetcher = vi.fn().mockImplementationOnce(() => new Promise(resolve => { finish = resolve }))
      .mockResolvedValue({ accounts: [{ account_id: 3, requests: [] }] })
    const state = setup(fetcher)
    const first = state.result.refresh()
    const signal = fetcher.mock.calls[0]?.[1] as AbortSignal
    state.ids.value = [3]
    expect(signal.aborted).toBe(true)
    await state.result.refresh()
    finish({ accounts: [{ account_id: 1, requests: [] }] })
    await first
    expect(state.result.requests.value).toEqual({ '3': [] })
    state.wrapper.unmount()
    expect(state.result.requests.value).toEqual({})
  })

  it('shows read failures instead of stale green results and recovers on the next refresh', async () => {
    const fetcher = vi.fn().mockRejectedValueOnce(new Error('busy')).mockResolvedValue({ accounts: [{ account_id: 1, requests: [] }] })
    const state = setup(fetcher)
    await state.result.refresh()
    expect(state.result.error.value).toBe(true)
    expect(state.result.loading.value).toBe(false)
    await state.result.refresh()
    expect(state.result.error.value).toBe(false)
    expect(state.result.requests.value).toEqual({ '1': [] })
    state.wrapper.unmount()
  })
})
