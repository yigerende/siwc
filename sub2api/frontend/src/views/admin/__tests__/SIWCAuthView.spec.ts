import { mount, flushPromises, enableAutoUnmount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import SIWCAuthView from '../SIWCAuthView.vue'
import { SIWC_REDIRECT_URI } from '@/api/admin/siwc'

enableAutoUnmount(afterEach)
const mocks = vi.hoisted(() => ({ groups: vi.fn(), connect: vi.fn(), disconnect: vi.fn(), generate: vi.fn(), importAccount: vi.fn() }))
vi.mock('@/api/admin/groups', () => ({ getAll: mocks.groups, default: { getAll: mocks.groups } }))
vi.mock('@/api/admin/siwc', async (original) => ({ ...await original<typeof import('@/api/admin/siwc')>(), siwcAPI: mocks }))
vi.mock('@/composables/useClipboard', () => ({ useClipboard: () => ({ copyToClipboard: vi.fn() }) }))
vi.mock('vue-i18n', async (original) => ({ ...await original<typeof import('vue-i18n')>(), useI18n: () => ({ t: (key: string) => key }) }))

const groupData = [
  { id: 2, name: 'First', platform: 'openai', status: 'active' },
  { id: 3, name: 'Second', platform: 'openai', status: 'active' }
]
const auth = {
  auth_url: `https://auth.openai.com/api/accounts/authorize?state=expected&redirect_uri=${encodeURIComponent(SIWC_REDIRECT_URI)}`,
  session_id: 'session', ext_agent_host_id: 'urn:uuid:host', registration: true
}
const callback = `${SIWC_REDIRECT_URI}?code=fake-code&state=expected&client_id=issued`
const render = () => mount(SIWCAuthView, { global: { stubs: { AppLayout: { template: '<main><slot /></main>' }, RouterLink: true } } })

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  mocks.groups.mockResolvedValue(groupData)
  mocks.generate.mockResolvedValue(auth)
  mocks.disconnect.mockResolvedValue(undefined)
  mocks.importAccount.mockResolvedValue({ id: 88, name: 'imported' })
  mocks.connect.mockResolvedValue({ target_id: 'remote-id', url: 'https://target.example', expires_at: new Date(Date.now() + 1800000).toISOString(), groups: [{ id: 9, name: 'Remote', platform: 'openai', status: 'active' }] })
})

describe('SIWC authorization page', () => {
  it('requires groups, locks the destination, and imports a matching callback to all selected groups', async () => {
    const w = render(); await flushPromises()
    expect(w.get('[data-testid="generate"]').attributes('disabled')).toBeDefined()
    await w.get('[data-testid="group-2"]').setValue(true)
    await w.get('[data-testid="group-3"]').setValue(true)
    await w.get('[data-testid="generate"]').trigger('click'); await flushPromises()
    expect(mocks.generate).toHaveBeenCalledWith(null, [2, 3], '')
    expect(w.get('fieldset').attributes('disabled')).toBeDefined()
    await w.get('[data-testid="callback"]').setValue(callback.replace('state=expected', 'state=wrong'))
    await w.get('[data-testid="import"]').trigger('click'); await flushPromises()
    expect(mocks.importAccount).not.toHaveBeenCalled()
    expect(w.get('[role="alert"]').text()).toContain('stateMismatch')
    await w.get('[data-testid="callback"]').setValue(callback)
    await w.get('[data-testid="import"]').trigger('click'); await flushPromises()
    expect(mocks.importAccount).toHaveBeenCalledWith(null, expect.objectContaining({ group_ids: [2, 3], session_id: 'session', code: 'fake-code', issued_client_id: 'issued' }))
    expect((w.get('[data-testid="callback"]').element as HTMLTextAreaElement).value).toBe('')
    expect(w.get('[data-testid="callback"]').attributes('disabled')).toBeDefined()
    expect(w.get('[role="status"]').text()).toContain('imported')
    expect(JSON.stringify(localStorage)).not.toContain('fake-code')
  })

  it('clears local groups when switching servers and sends remote imports to the connected target', async () => {
    const w = render(); await flushPromises()
    await w.get('[data-testid="group-2"]').setValue(true)
    await w.get('input[value="remote"]').setValue(true); await flushPromises()
    expect(w.find('[data-testid="group-2"]').exists()).toBe(false)
    await w.get('[data-testid="remote-url"]').setValue('https://target.example')
    await w.get('[data-testid="remote-email"]').setValue('admin@example.com')
    await w.get('[data-testid="remote-password"]').setValue('test-password')
    await w.get('[data-testid="connect"]').trigger('click'); await flushPromises()
    expect((w.get('[data-testid="remote-password"]').element as HTMLInputElement).value).toBe('')
    expect(w.get('[data-testid="generate"]').attributes('disabled')).toBeDefined()
    await w.get('[data-testid="group-9"]').setValue(true)
    await w.get('[data-testid="generate"]').trigger('click'); await flushPromises()
    expect(mocks.generate).toHaveBeenCalledWith('remote-id', [9], '')
    await w.get('[data-testid="callback"]').setValue(callback)
    await w.get('[data-testid="import"]').trigger('click'); await flushPromises()
    expect(mocks.importAccount).toHaveBeenCalledWith('remote-id', expect.objectContaining({ group_ids: [9] }))
  })

  it('prevents replay after an ambiguous failed import', async () => {
    mocks.importAccount.mockRejectedValue(new Error('timeout'))
    const w = render(); await flushPromises()
    await w.get('[data-testid="group-2"]').setValue(true)
    await w.get('[data-testid="generate"]').trigger('click'); await flushPromises()
    await w.get('[data-testid="callback"]').setValue(callback)
    await w.get('[data-testid="import"]').trigger('click'); await flushPromises()
    expect(w.get('[data-testid="import"]').attributes('disabled')).toBeDefined()
    expect((w.get('[data-testid="callback"]').element as HTMLTextAreaElement).value).toBe('')
    expect(w.text()).toContain('checkBeforeRetry')
    expect(mocks.importAccount).toHaveBeenCalledTimes(1)
  })
})
