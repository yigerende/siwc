import { mount, flushPromises } from '@vue/test-utils'
import { describe, it, expect, vi } from 'vitest'
import SIWCUsageCell from '../SIWCUsageCell.vue'

const { post } = vi.hoisted(() => ({ post: vi.fn() }))
vi.mock('@/api/client', () => ({ apiClient: { post } }))

describe('SIWC web quota display', () => {
  it('shows only the provided window and no year-one timestamp after refresh', async () => {
    post.mockResolvedValue({ data: {
      checked_at: '2026-11-12T12:00:00Z',
      rate_limit: { secondary_window: { limit_window_seconds: 604800, used_percent: 17, reset_at: 1795087944 } }
    } })
    const wrapper = mount(SIWCUsageCell, {
      props: { account: { id: 6, credentials: { auth_mode: 'siwc' }, extra: { siwc_usage: { checked_at: '0001-01-01T00:00:00Z', error: 'failed' } } } },
      global: { stubs: { UsageProgressBar: { props: ['label', 'utilization'], template: '<span data-quota>{{label}} {{utilization}}%</span>' } } }
    })
    expect(wrapper.text()).not.toContain('1/1/')
    await wrapper.get('button').trigger('click')
    await flushPromises()
    expect(post).toHaveBeenCalledWith('/admin/siwc/accounts/6/usage')
    expect(wrapper.findAll('[data-quota]')).toHaveLength(1)
    expect(wrapper.text()).toContain('7d 17%')
    expect(wrapper.text()).toContain('5h：未提供')
    expect(wrapper.text()).not.toContain('0%')
    expect(wrapper.find('details').exists()).toBe(false)
  })
})
