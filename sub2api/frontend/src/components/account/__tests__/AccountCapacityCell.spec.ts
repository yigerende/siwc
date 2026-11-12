import { mount } from '@vue/test-utils'
import { describe, expect, it, vi } from 'vitest'
import Cell from '../AccountCapacityCell.vue'
import type { Account } from '@/types'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('account request RPM', () => {
  it('distinguishes zero from unavailable and updates independently of concurrency', async () => {
    const account = { platform: 'openai', type: 'oauth', concurrency: 18, current_concurrency: 3, request_rpm: 0 } as Account
    const wrapper = mount(Cell, { props: { account } })
    expect(wrapper.get('[data-testid="account-request-rpm"]').text()).toBe('RPM: 0')
    await wrapper.setProps({ account: { ...account, request_rpm: 42 } })
    expect(wrapper.get('[data-testid="account-request-rpm"]').text()).toBe('RPM: 42')
    expect(wrapper.text()).toContain('3/18')
    await wrapper.setProps({ account: { ...account, request_rpm: null } })
    expect(wrapper.get('[data-testid="account-request-rpm"]').text()).toBe('RPM: --')
    await wrapper.setProps({ account: { ...account, request_rpm: undefined } })
    expect(wrapper.get('[data-testid="account-request-rpm"]').text()).toBe('RPM: --')
  })
})
