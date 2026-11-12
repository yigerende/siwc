import { describe, expect, it, vi } from 'vitest'
import { mount } from '@vue/test-utils'
import AccountRecentRequestsCell from '../AccountRecentRequestsCell.vue'

vi.mock('vue-i18n', async () => ({
  ...await vi.importActual<typeof import('vue-i18n')>('vue-i18n'),
  useI18n: () => ({ t: (key: string) => key })
}))

const requests = [
  { created_at: '2026-09-18T12:00:00Z', failed: false },
  { created_at: '2026-09-18T11:00:00Z', failed: true, status_code: 429, error_message: 'Rate limit <script>unsafe</script>' }
]

describe('AccountRecentRequestsCell', () => {
  it('shows ten stable slots, gray padding on the left and newest on the right', () => {
    const wrapper = mount(AccountRecentRequestsCell, { props: { requests } })
    const bars = wrapper.findAll('[data-request-status]')
    expect(bars).toHaveLength(10)
    expect(bars.map(bar => bar.attributes('data-request-status'))).toEqual([...Array(8).fill('empty'), 'failed', 'success'])
    expect(bars[8]?.attributes('title')).toContain('HTTP 429')
    expect(wrapper.find('script').exists()).toBe(false)
  })

  it('shows the latest error even after newer successful requests', () => {
    const wrapper = mount(AccountRecentRequestsCell, { props: { requests, errorOnly: true } })
    expect(wrapper.text()).toContain('HTTP 429')
    expect(wrapper.text()).toContain('Rate limit')
    expect(wrapper.find('script').exists()).toBe(false)
  })

  it('distinguishes empty history from a failed read', async () => {
    const wrapper = mount(AccountRecentRequestsCell)
    expect(wrapper.findAll('[data-request-status="empty"]')).toHaveLength(10)
    await wrapper.setProps({ error: true })
    expect(wrapper.findAll('[data-request-status]')).toHaveLength(0)
    expect(wrapper.text()).toContain('unavailable')
  })

  it('retains recovered error diagnostics on a green success', () => {
    const wrapper = mount(AccountRecentRequestsCell, { props: { requests: [{ ...requests[1]!, failed: false }] } })
    expect(wrapper.get('[data-request-status="success"]').attributes('title')).toContain('recovered')
  })
})
