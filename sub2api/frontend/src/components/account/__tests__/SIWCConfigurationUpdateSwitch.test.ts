import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import SIWCConfigurationUpdateSwitch from '../SIWCConfigurationUpdateSwitch.vue'

const api = vi.hoisted(() => ({ configurationUpdate: vi.fn(), saveConfigurationUpdate: vi.fn() }))
vi.mock('@/api/admin/siwc', () => ({ siwcAPI: api }))

describe('SIWC configuration update switch', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    api.configurationUpdate.mockResolvedValue({ enabled: false })
  })
  it('loads without saving or enabling and disables during load', async () => {
    const wrapper = mount(SIWCConfigurationUpdateSwitch)
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeDefined()
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('false')
    expect(api.saveConfigurationUpdate).not.toHaveBeenCalled()
  })
  it('saves only once and waits for server acknowledgement', async () => {
    let resolve!: (value: { enabled: boolean }) => void
    api.saveConfigurationUpdate.mockReturnValue(new Promise(r => { resolve = r }))
    const wrapper = mount(SIWCConfigurationUpdateSwitch)
    await flushPromises()
    await wrapper.get('[role=switch]').trigger('click')
    await wrapper.get('[role=switch]').trigger('click')
    expect(api.saveConfigurationUpdate).toHaveBeenCalledTimes(1)
    expect(api.saveConfigurationUpdate).toHaveBeenCalledWith(true)
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('false')
    resolve({ enabled: true })
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('true')
    expect(wrapper.emitted()).not.toHaveProperty('refresh')
  })
  it('does not misrepresent a failed save and re-reads before another toggle', async () => {
    api.saveConfigurationUpdate.mockRejectedValue(new Error('timeout'))
    const wrapper = mount(SIWCConfigurationUpdateSwitch)
    await flushPromises()
    await wrapper.get('[role=switch]').trigger('click')
    await flushPromises()
    expect(wrapper.text()).toContain('状态未确认')
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeDefined()
    api.configurationUpdate.mockResolvedValue({ enabled: true })
    await wrapper.get('[role=alert]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('aria-checked')).toBe('true')
  })
  it('offers retry on a failed initial read', async () => {
    api.configurationUpdate.mockRejectedValueOnce(new Error('offline'))
    const wrapper = mount(SIWCConfigurationUpdateSwitch)
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeDefined()
    await wrapper.get('[role=alert]').trigger('click')
    await flushPromises()
    expect(wrapper.get('[role=switch]').attributes('disabled')).toBeUndefined()
    expect(api.saveConfigurationUpdate).not.toHaveBeenCalled()
  })
})
