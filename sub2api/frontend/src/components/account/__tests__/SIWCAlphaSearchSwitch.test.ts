import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SIWCAlphaSearchSwitch from '../SIWCAlphaSearchSwitch.vue'

const api = vi.hoisted(() => ({ alphaSearch: vi.fn(), saveAlphaSearch: vi.fn() }))
vi.mock('@/api/admin/siwc', () => ({ siwcAPI: api }))

describe('SIWC alpha search switch', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    api.alphaSearch.mockResolvedValue({ enabled: false })
  })
  it('loads without saving or enabling and disables during load', async () => {
    const wrapper = mount(SIWCAlphaSearchSwitch)
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeDefined()
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('false')
    expect(api.saveAlphaSearch).not.toHaveBeenCalled()
  })
  it('saves only once and waits for server acknowledgement', async () => {
    let resolve!: (value: { enabled: boolean }) => void
    api.saveAlphaSearch.mockReturnValue(new Promise(r => { resolve = r }))
    const wrapper = mount(SIWCAlphaSearchSwitch)
    await flushPromises()
    await wrapper.get('[role=switch]').trigger('click')
    await wrapper.get('[role=switch]').trigger('click')
    expect(api.saveAlphaSearch).toHaveBeenCalledTimes(1)
    expect(api.saveAlphaSearch).toHaveBeenCalledWith(true)
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('false')
    resolve({ enabled: true })
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('true')
    expect(wrapper.emitted()).not.toHaveProperty('refresh')
  })
  it('does not misrepresent a failed save and re-reads before another toggle', async () => {
    api.saveAlphaSearch.mockRejectedValue(new Error('timeout'))
    const wrapper = mount(SIWCAlphaSearchSwitch)
    await flushPromises()
    await wrapper.get('[role=switch]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('状态未确认')
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeDefined()
    api.alphaSearch.mockResolvedValue({ enabled: true })
    await wrapper.get('[role=alert]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('true')
  })
  it('offers retry on a failed initial read', async () => {
    api.alphaSearch.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mount(SIWCAlphaSearchSwitch)
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeDefined()
    await wrapper.get('[role=alert]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeUndefined()
    expect(api.saveAlphaSearch).not.toHaveBeenCalled()
  })
})
