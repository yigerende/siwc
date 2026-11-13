import { mount, flushPromises } from '@vue/test-utils'
import { describe, expect, it, vi, beforeEach } from 'vitest'
import Badge from '../SpaceWorkflowBadge.vue'
import Cleanup from '../SpaceWorkflowCleanup.vue'
import api from '@/api/client'

vi.mock('@/api/client', () => ({ default: { get: vi.fn(), put: vi.fn() } }))
const marker = { source_id: 'source', revision: 1, email: 'a@example.com', flow_id: 'flow', order_id: 'order', stage: 'merge_v2' as const, completed: true, manual: false, changed_at: '2026-11-13T08:00:00Z' }

describe('Space completion display and automatic cleanup', () => {
  beforeEach(() => vi.resetAllMocks())
  it('shows accessible expandable details for completed markers only', async () => {
    const wrapper = mount(Badge, { props: { marker } })
    expect(wrapper.get('summary').text()).toContain('Space 已完成')
    expect(wrapper.get('p').text()).toContain('合并2.0')
    expect(wrapper.get('p').text()).toContain('order')
    await wrapper.setProps({ marker: { ...marker, completed: false } })
    expect(wrapper.find('details').exists()).toBe(false)
  })
  it('keeps default disabled and persists explicit administrator settings', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { enabled: false, retain_minutes: 60, interval_minutes: 5 } })
    vi.mocked(api.put).mockResolvedValue({ data: { enabled: true, retain_minutes: 20, interval_minutes: 3 } })
    const wrapper = mount(Cleanup)
    await flushPromises()
    expect(wrapper.get('summary').text()).toContain('已关闭')
    await wrapper.get('input[type=checkbox]').setValue(true)
    await wrapper.get('[aria-label="完成后保留分钟"]').setValue(20)
    await wrapper.get('[aria-label="清理间隔分钟"]').setValue(3)
    await wrapper.get('form').trigger('submit')
    await flushPromises()
    expect(api.put).toHaveBeenCalledWith('/admin/siwc/space-cleanup', { enabled: true, retain_minutes: 20, interval_minutes: 3 })
    expect(wrapper.get('summary').text()).toContain('已开启')
  })
  it('requires reread after an ambiguous save and blocks invalid input', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { enabled: false, retain_minutes: 60, interval_minutes: 5 } })
    vi.mocked(api.put).mockRejectedValue(new Error('timeout'))
    const wrapper = mount(Cleanup); await flushPromises()
    await wrapper.get('[aria-label="清理间隔分钟"]').setValue(0)
    await wrapper.get('form').trigger('submit'); expect(api.put).not.toHaveBeenCalled()
    await wrapper.get('[aria-label="清理间隔分钟"]').setValue(5)
    await wrapper.get('form').trigger('submit'); await flushPromises()
    expect(wrapper.get('[role=alert]').text()).toContain('保存结果未确认')
    expect(wrapper.get('button[type=submit]').attributes()).toHaveProperty('disabled')
  })
})
