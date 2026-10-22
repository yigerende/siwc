import { describe, expect, it } from 'vitest'
import { parseSIWCCallback, validateSIWCAuthURL } from '../siwcCallback'
import { SIWC_REDIRECT_URI } from '@/api/admin/siwc'

const auth = `https://auth.openai.com/api/accounts/authorize?state=expected&redirect_uri=${encodeURIComponent(SIWC_REDIRECT_URI)}`
const callback = `${SIWC_REDIRECT_URI}?code=single-use-code&state=expected&client_id=issued-client&scope=openid+email`
describe('SIWC manual callback', () => {
  it('extracts the complete callback without navigating or exchanging it', () => {
    expect(parseSIWCCallback(` ${callback} `, auth)).toEqual({ code: 'single-use-code', state: 'expected', issued_client_id: 'issued-client' })
  })
  it('rejects unrelated, partial, ambiguous, or declined callbacks', () => {
    expect(() => parseSIWCCallback(callback.replace('state=expected', 'state=old'), auth)).toThrow('stateMismatch')
    expect(() => parseSIWCCallback(callback.replace('&client_id=issued-client', ''), auth)).toThrow('incompleteCallback')
    expect(() => parseSIWCCallback(callback + '&code=second', auth)).toThrow('incompleteCallback')
    expect(() => parseSIWCCallback(callback.replace(':1456', ':1455'), auth)).toThrow('invalidCallback')
    expect(() => parseSIWCCallback('just-the-code', auth)).toThrow('invalidCallback')
    expect(() => parseSIWCCallback(SIWC_REDIRECT_URI + '?error=access_denied', auth)).toThrow('authorizationDenied')
  })
  it('only presents links from the expected authorization server', () => {
    expect(validateSIWCAuthURL(auth)).toBe(true)
    expect(validateSIWCAuthURL(auth.replace('auth.openai.com', 'attacker.example'))).toBe(false)
    expect(validateSIWCAuthURL('javascript:alert(1)')).toBe(false)
    expect(validateSIWCAuthURL(auth.replace('https:', 'http:'))).toBe(false)
  })
})
