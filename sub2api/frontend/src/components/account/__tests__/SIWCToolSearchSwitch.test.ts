import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SIWCToolSearchSwitch from '../SIWCToolSearchSwitch.vue'

const api = vi.hoisted(() => ({ toolSearch: vi.fn(), saveToolSearch: vi.fn() }))
vi.mock('@/api/admin/siwc', () => ({ siwcAPI: api }))

describe('SIWC remote compaction switch', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    api.toolSearch.mockResolvedValue({ enabled: false })
  })
  it('loads without saving or enabling and disables during load', async () => {
    const wrapper = mount(SIWCToolSearchSwitch)
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeDefined()
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('false')
    expect(api.saveToolSearch).not.toHaveBeenCalled()
  })
  it('saves only once and waits for server acknowledgement', async () => {
    let resolve!: (value: { enabled: boolean }) => void
    api.saveToolSearch.mockReturnValue(new Promise(r => { resolve = r }))
    const wrapper = mount(SIWCToolSearchSwitch)
    await flushPromises()
    await wrapper.get('[role=switch]').trigger('click')
    await wrapper.get('[role=switch]').trigger('click')
    expect(api.saveToolSearch).toHaveBeenCalledTimes(1)
    expect(api.saveToolSearch).toHaveBeenCalledWith(true)
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('false')
    resolve({ enabled: true })
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('true')
    expect(wrapper.emitted()).not.toHaveProperty('refresh')
  })
  it('does not misrepresent a failed save and re-reads before another toggle', async () => {
    api.saveToolSearch.mockRejectedValue(new Error('timeout'))
    const wrapper = mount(SIWCToolSearchSwitch)
    await flushPromises()
    await wrapper.get('[role=switch]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('状态未确认')
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeDefined()
    api.toolSearch.mockResolvedValue({ enabled: true })
    await wrapper.get('[role=alert]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('true')
  })
  it('offers retry on a failed initial read', async () => {
    api.toolSearch.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mount(SIWCToolSearchSwitch)
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeDefined()
    await wrapper.get('[role=alert]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeUndefined()
    expect(api.saveToolSearch).not.toHaveBeenCalled()
  })
})
