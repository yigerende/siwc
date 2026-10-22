import { apiClient } from '../client'

export const SIWC_REDIRECT_URI = 'http://127.0.0.1:1456/auth/callback'
const root = '/admin/openai/siwc'
export interface SIWCGroup { id: number; name: string; platform: string; status: string }
export interface SIWCTarget { target_id: string; url: string; expires_at: string; groups: SIWCGroup[] }
export interface SIWCAuth { auth_url: string; session_id: string; ext_agent_host_id: string; registration: boolean }
export interface SIWCImport {
  session_id: string
  code: string
  state: string
  issued_client_id: string
  redirect_uri: string
  group_ids: number[]
  name: string
}
export const siwcAPI = {
  async connect(url: string, email: string, password: string): Promise<SIWCTarget> {
    return (await apiClient.post(`${root}/remote/connect`, { url, email, password }, { timeout: 100000 })).data
  },
  async disconnect(id: string): Promise<void> { await apiClient.delete(`${root}/remote/${encodeURIComponent(id)}`) },
  async generate(target: string | null, groupIDs: number[], hostID: string): Promise<SIWCAuth> {
    const path = target ? `${root}/remote/${encodeURIComponent(target)}/generate` : `${root}/generate-auth-url`
    return (await apiClient.post(path, {
      group_ids: groupIDs, ext_agent_host_id: hostID, redirect_uri: SIWC_REDIRECT_URI, agent_name_hint: 'sub2api'
    }, { timeout: 100000 })).data
  },
  async importAccount(target: string | null, body: SIWCImport): Promise<{ id: number; name: string }> {
    const path = target ? `${root}/remote/${encodeURIComponent(target)}/import` : `${root}/create-from-oauth`
    return (await apiClient.post(path, body, { timeout: 100000 })).data
  }
}
