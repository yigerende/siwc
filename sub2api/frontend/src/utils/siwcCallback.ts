import { SIWC_REDIRECT_URI } from '@/api/admin/siwc'

// Parse locally: never navigate to, log, or persist a callback containing a code.
export function parseSIWCCallback(value: string, authURL: string) {
  let callback: URL
  let auth: URL
  try { callback = new URL(value.trim()); auth = new URL(authURL) } catch { throw new Error('invalidCallback') }
  if (callback.origin + callback.pathname !== SIWC_REDIRECT_URI || callback.username || callback.password || callback.hash) {
    throw new Error('invalidCallback')
  }
  if (callback.searchParams.has('error')) throw new Error('authorizationDenied')
  for (const key of ['code', 'state', 'client_id']) {
    if (callback.searchParams.getAll(key).length !== 1 || !callback.searchParams.get(key)?.trim()) throw new Error('incompleteCallback')
  }
  if (callback.searchParams.get('state') !== auth.searchParams.get('state')) throw new Error('stateMismatch')
  return {
    code: callback.searchParams.get('code')!,
    state: callback.searchParams.get('state')!,
    issued_client_id: callback.searchParams.get('client_id')!
  }
}

export function validateSIWCAuthURL(value: string): boolean {
  try {
    const url = new URL(value)
    return url.protocol === 'https:' && url.host === 'auth.openai.com' && !url.username && !url.password &&
      !!url.searchParams.get('state') && url.searchParams.get('redirect_uri') === SIWC_REDIRECT_URI
  } catch { return false }
}
