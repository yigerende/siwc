<template>
  <AppLayout>
    <div class="mx-auto max-w-4xl space-y-6">
      <header class="border-b border-gray-200 pb-5 dark:border-dark-700">
        <h2 class="text-lg font-semibold text-gray-900 dark:text-white">{{ t('admin.siwc.title') }}</h2>
        <p class="mt-2 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.siwc.description') }}</p>
      </header>

      <div v-if="error" role="alert" class="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700 dark:border-red-900 dark:bg-red-950/30 dark:text-red-300">{{ error }}</div>
      <div v-if="success" role="status" class="rounded-lg border border-green-200 bg-green-50 p-4 text-sm text-green-800 dark:border-green-900 dark:bg-green-950/30 dark:text-green-200">
        {{ t('admin.siwc.imported', success) }}
        <RouterLink v-if="success.local" to="/admin/accounts" class="ml-2 underline">{{ t('admin.siwc.viewAccounts') }}</RouterLink>
      </div>

      <section class="rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800">
        <h3 class="mb-4 font-semibold text-gray-900 dark:text-white">{{ t('admin.siwc.stepTarget') }}</h3>
        <fieldset :disabled="!!busy || !!pending" class="space-y-5 disabled:opacity-70">
          <div class="flex flex-wrap gap-5 text-sm">
            <label class="flex items-center gap-2"><input v-model="mode" type="radio" value="local" name="target" />{{ t('admin.siwc.currentTarget') }}</label>
            <label class="flex items-center gap-2"><input v-model="mode" type="radio" value="remote" name="target" />{{ t('admin.siwc.remoteTarget') }}</label>
          </div>
          <div v-if="mode === 'remote'" class="space-y-3">
            <label class="block"><span class="input-label">{{ t('admin.siwc.serverURL') }}</span><input v-model.trim="remoteURL" data-testid="remote-url" type="url" class="input" placeholder="https://sub2api.example.com" autocomplete="url" /></label>
            <div class="grid gap-3 sm:grid-cols-2">
              <label><span class="input-label">{{ t('admin.siwc.email') }}</span><input v-model.trim="email" data-testid="remote-email" type="email" class="input" autocomplete="username" /></label>
              <label><span class="input-label">{{ t('admin.siwc.password') }}</span><input v-model="password" data-testid="remote-password" type="password" class="input" autocomplete="off" /></label>
            </div>
            <p class="text-xs text-gray-500">{{ t('admin.siwc.remoteHint') }}</p>
            <button v-if="!remoteTarget" type="button" data-testid="connect" class="btn btn-secondary" :disabled="!remoteURL || !email || !password" @click="connect">{{ t('admin.siwc.connect') }}</button>
            <div v-else class="flex flex-wrap items-center gap-3 text-sm text-green-700 dark:text-green-300">
              <span>{{ t('admin.siwc.connected', { url: remoteTarget.url }) }}</span>
              <button type="button" class="underline" @click="disconnect">{{ t('admin.siwc.disconnect') }}</button>
            </div>
          </div>
          <div v-if="mode === 'local' || remoteTarget">
            <div class="mb-2 flex items-center justify-between">
              <span class="input-label mb-0">{{ t('admin.siwc.groups') }}</span>
              <button v-if="mode === 'local'" type="button" class="text-xs text-primary-600" @click="loadLocalGroups">{{ t('common.refresh') }}</button>
            </div>
            <div class="grid max-h-56 gap-2 overflow-y-auto sm:grid-cols-2">
              <label v-for="group in groups" :key="group.id" class="flex cursor-pointer items-center gap-3 rounded-lg border border-gray-200 p-3 text-sm dark:border-dark-600" :class="selectedGroups.includes(group.id) ? 'bg-primary-50 dark:bg-primary-950/20' : ''">
                <input v-model="selectedGroups" type="checkbox" :value="group.id" :data-testid="`group-${group.id}`" class="rounded border-gray-300 text-primary-600" />
                <span class="min-w-0 flex-1 truncate">{{ group.name }}</span><span class="text-xs text-gray-400">#{{ group.id }}</span>
              </label>
            </div>
            <p v-if="!groups.length && !busy" class="py-3 text-sm text-gray-500">{{ t('admin.siwc.noGroups') }}</p>
            <p class="mt-2 text-xs text-gray-500">{{ t('admin.siwc.groupsHint', { count: selectedGroups.length }) }}</p>
          </div>
          <label class="block"><span class="input-label">{{ t('admin.siwc.accountName') }}</span><input v-model.trim="accountName" class="input" :placeholder="t('admin.siwc.accountNameHint')" maxlength="100" /></label>
        </fieldset>
        <p v-if="busy" role="status" class="mt-4 text-sm text-gray-500">{{ t('common.processing') }}</p>
        <p v-if="pending" class="mt-4 text-xs text-gray-500">{{ t('admin.siwc.targetLocked') }}</p>
      </section>

      <section class="rounded-lg border border-blue-200 bg-blue-50 p-4 dark:border-blue-800 dark:bg-blue-950/20 sm:p-5">
        <div class="mb-4 flex items-center gap-3">
          <div class="rounded-lg bg-blue-500 p-2 text-white"><Icon name="link" size="md" /></div>
          <div><h3 class="font-semibold text-blue-900 dark:text-blue-200">{{ t('admin.siwc.panelTitle') }}</h3><p class="mt-1 text-xs text-blue-700 dark:text-blue-300">{{ t('admin.siwc.panelHint') }}</p></div>
        </div>
        <div class="space-y-3 sm:ml-11">
          <div class="siwc-step">
            <span class="siwc-number">1</span>
            <div class="min-w-0 flex-1">
              <p class="siwc-step-title">{{ t('admin.siwc.stepGenerate') }}</p>
              <button v-if="!pending" type="button" data-testid="generate" class="btn btn-primary mt-3" :disabled="!!busy || !selectedGroups.length || (mode === 'remote' && !remoteTarget)" @click="generate"><Icon name="link" size="sm" />{{ t('admin.siwc.generate') }}</button>
              <template v-else>
                <p class="mt-2 text-xs text-blue-700 dark:text-blue-300">{{ t('admin.siwc.destination', { target: pending.targetLabel, groups: pending.groupNames.join('、') }) }}</p>
                <p class="mt-2 text-xs text-gray-500">{{ t('admin.siwc.expiry', { time: new Date(pending.expiresAt).toLocaleTimeString() }) }}</p>
              </template>
            </div>
          </div>
          <div class="siwc-step">
            <span class="siwc-number">2</span>
            <div class="min-w-0 flex-1">
              <p class="siwc-step-title">{{ t('admin.siwc.stepAuthorize') }}</p>
              <p class="mt-2 text-xs text-blue-700 dark:text-blue-300">{{ t('admin.siwc.browserHint') }}</p>
              <template v-if="pending">
                <textarea :value="pending.auth.auth_url" readonly rows="2" class="input mt-3 break-all text-xs" :aria-label="t('admin.siwc.authLink')" />
                <div class="mt-3 flex flex-wrap gap-3">
                  <a :href="pending.auth.auth_url" target="_blank" rel="noopener noreferrer" referrerpolicy="no-referrer" class="btn btn-primary">{{ t('admin.siwc.openLink') }}</a>
                  <button type="button" class="btn btn-secondary" @click="copyToClipboard(pending.auth.auth_url)">{{ t('admin.siwc.copyLink') }}</button>
                </div>
              </template>
              <p class="mt-3 rounded border border-amber-200 bg-amber-50 px-3 py-2 text-xs leading-6 text-amber-800 dark:border-amber-900 dark:bg-amber-950/30 dark:text-amber-200">{{ t('admin.siwc.callbackHint') }}</p>
            </div>
          </div>
          <div class="siwc-step">
            <span class="siwc-number">3</span>
            <div class="min-w-0 flex-1">
              <p class="siwc-step-title">{{ t('admin.siwc.stepImport') }}</p>
              <label class="mt-3 block"><span class="input-label">{{ t('admin.siwc.callbackLabel') }}</span><textarea v-model="callback" data-testid="callback" rows="3" class="input text-sm" spellcheck="false" autocomplete="off" :disabled="!pending || !!busy || pending.submitted" placeholder="http://127.0.0.1:1456/auth/callback?code=…&state=…&client_id=…" /></label>
              <p v-if="expired" class="mt-2 text-sm text-red-600">{{ t('admin.siwc.expired') }}</p>
              <p v-if="pending?.submitted && !busy" class="mt-2 text-sm text-amber-700 dark:text-amber-300">{{ t('admin.siwc.checkBeforeRetry') }}</p>
            </div>
          </div>
        </div>
        <div class="mt-5 flex justify-end gap-3 border-t border-blue-200 pt-4 dark:border-blue-800">
          <button v-if="pending" type="button" data-testid="reset" class="btn btn-secondary" :disabled="!!busy" @click="reset">{{ t('admin.siwc.reset') }}</button>
          <button type="button" data-testid="import" class="btn btn-primary" :disabled="!pending || !!busy || !callback.trim() || pending.submitted || expired" @click="importAccount">{{ busy === 'import' ? t('common.processing') : t('admin.siwc.import') }}</button>
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { getAll } from '@/api/admin/groups'
import { siwcAPI, SIWC_REDIRECT_URI, type SIWCAuth, type SIWCGroup, type SIWCTarget } from '@/api/admin/siwc'
import { parseSIWCCallback, validateSIWCAuthURL } from '@/utils/siwcCallback'
import { useClipboard } from '@/composables/useClipboard'

const { t } = useI18n()
const { copyToClipboard } = useClipboard()
const mode = ref<'local' | 'remote'>('local')
const remoteURL = ref('')
const email = ref('')
const password = ref('')
const remoteTarget = ref<SIWCTarget | null>(null)
const groups = ref<SIWCGroup[]>([])
const selectedGroups = ref<number[]>([])
const accountName = ref('')
const busy = ref('')
const error = ref('')
const callback = ref('')
const success = ref<{ id: number; name: string; target: string; groups: string; local: boolean } | null>(null)
const now = ref(Date.now())
const pending = ref<{
  auth: SIWCAuth; targetID: string | null; targetLabel: string; groupIDs: number[]; groupNames: string[]
  name: string; expiresAt: number; submitted: boolean
} | null>(null)
const expired = computed(() => !!pending.value && now.value >= pending.value.expiresAt)
let mounted = true
const timer = window.setInterval(() => { now.value = Date.now() }, 1000)

function message(e: unknown) { return (e as { message?: string })?.message || t('admin.siwc.requestFailed') }
function reset() { pending.value = null; callback.value = ''; error.value = '' }
function disconnect() {
  const target = remoteTarget.value
  remoteTarget.value = null
  if (target) void siwcAPI.disconnect(target.target_id).catch(() => {})
  groups.value = []; selectedGroups.value = []; reset()
}
async function loadLocalGroups() {
  busy.value = 'groups'; error.value = ''
  try {
    groups.value = (await getAll('openai')).filter(g => g.platform === 'openai' && g.status === 'active')
    selectedGroups.value = selectedGroups.value.filter(id => groups.value.some(g => g.id === id))
  } catch (e) { groups.value = []; selectedGroups.value = []; error.value = message(e) }
  finally { busy.value = '' }
}
watch(mode, () => { disconnect(); success.value = null; if (mode.value === 'local') void loadLocalGroups() })
watch([remoteURL, email], () => { if (remoteTarget.value) disconnect() })
async function connect() {
  busy.value = 'connect'; error.value = ''; success.value = null
  try {
    const result = await siwcAPI.connect(remoteURL.value, email.value, password.value)
    if (!mounted) { void siwcAPI.disconnect(result.target_id).catch(() => {}); return }
    remoteTarget.value = result; groups.value = result.groups; selectedGroups.value = []
  } catch (e) { error.value = message(e) }
  finally { password.value = ''; busy.value = '' }
}
async function generate() {
  if (!selectedGroups.value.length || busy.value) return
  if (mode.value === 'remote' && (!remoteTarget.value || Date.now() >= Date.parse(remoteTarget.value.expires_at))) {
    disconnect(); error.value = t('admin.siwc.connectionExpired'); return
  }
  busy.value = 'generate'; error.value = ''; success.value = null
  const targetID = mode.value === 'remote' ? remoteTarget.value!.target_id : null
  const targetLabel = targetID ? remoteTarget.value!.url : t('admin.siwc.currentTarget')
  const key = `siwc_host_id:${targetID ? remoteTarget.value!.url : window.location.origin}`
  let hostID = ''
  try { hostID = localStorage.getItem(key) || '' } catch { /* Storage may be unavailable. */ }
  try {
    const result = await siwcAPI.generate(targetID, [...selectedGroups.value], hostID)
    if (!mounted) return
    if (!validateSIWCAuthURL(result.auth_url)) throw new Error(t('admin.siwc.invalidAuthLink'))
    try { localStorage.setItem(key, result.ext_agent_host_id) } catch { /* Non-secret host ID only. */ }
    pending.value = {
      auth: result, targetID, targetLabel, groupIDs: [...selectedGroups.value],
      groupNames: groups.value.filter(g => selectedGroups.value.includes(g.id)).map(g => g.name), name: accountName.value,
      expiresAt: Math.min(Date.now() + 30 * 60 * 1000, targetID ? Date.parse(remoteTarget.value!.expires_at) : Infinity), submitted: false
    }
    callback.value = ''; now.value = Date.now()
  } catch (e) { error.value = message(e) }
  finally { busy.value = '' }
}
async function importAccount() {
  const session = pending.value
  if (!session || busy.value || session.submitted) return
  if (Date.now() >= session.expiresAt) { error.value = t('admin.siwc.expired'); return }
  let parsed: ReturnType<typeof parseSIWCCallback>
  try { parsed = parseSIWCCallback(callback.value, session.auth.auth_url) }
  catch (e) { error.value = t(`admin.siwc.${message(e)}`); return }
  busy.value = 'import'; error.value = ''; session.submitted = true
  try {
    const result = await siwcAPI.importAccount(session.targetID, {
      ...parsed, session_id: session.auth.session_id, redirect_uri: SIWC_REDIRECT_URI, group_ids: session.groupIDs, name: session.name
    })
    success.value = { ...result, target: session.targetLabel, groups: session.groupNames.join('、'), local: !session.targetID }
    reset()
  } catch (e) { error.value = message(e) }
  finally { callback.value = ''; busy.value = '' }
}
onMounted(loadLocalGroups)
onBeforeUnmount(() => { mounted = false; window.clearInterval(timer); password.value = ''; callback.value = ''; disconnect() })
</script>

<style scoped>
.siwc-step { @apply flex gap-3 rounded-lg border border-blue-300 bg-white/80 p-4 dark:border-blue-800 dark:bg-dark-800; }
.siwc-number { @apply flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-blue-600 text-xs font-semibold text-white; }
.siwc-step-title { @apply text-sm font-medium text-blue-900 dark:text-blue-200; }
</style>
