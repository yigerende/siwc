<script setup lang="ts">
import {computed,ref,watch} from 'vue'
import {apiClient} from '@/api/client'
import UsageProgressBar from './UsageProgressBar.vue'
const props=defineProps<{account:any}>()
const isSIWC=computed(()=>props.account.credentials?.auth_mode==='siwc'||props.account.extra?.siwc_usage!=null)
const usage=ref<any>(null),busy=ref(false),error=ref('')
watch(()=>props.account,()=>{usage.value=props.account.extra?.siwc_usage??null},{immediate:true})
const windows=computed(()=>[usage.value?.rate_limit?.primary_window,usage.value?.rate_limit?.secondary_window].filter(Boolean))
const missingWindows=computed(()=>windows.value.length ? [18000,604800].filter(seconds=>!windows.value.some((w:any)=>w.limit_window_seconds===seconds)) : [])
const checkedAt=computed(()=>usage.value?.checked_at && new Date(usage.value.checked_at).getFullYear()>1970 ? new Date(usage.value.checked_at).toLocaleString() : '')
function date(v:number){return new Date(v*1000).toISOString()}
async function refresh(){busy.value=true;error.value='';try{usage.value=(await apiClient.post(`/admin/siwc/accounts/${props.account.id}/usage`)).data}catch(e:any){error.value=e.message}finally{busy.value=false}}
</script>
<template><div v-if="isSIWC" class="mt-2 space-y-1 border-t border-gray-200 pt-2 text-xs dark:border-dark-600"><div class="flex justify-between gap-3"><span>SIWC 额度</span><button class="text-primary-500" :disabled="busy" @click="refresh">{{busy?'查询中…':'刷新'}}</button></div><UsageProgressBar v-for="w in windows" :key="w.limit_window_seconds" :label="w.limit_window_seconds===18000?'5h':'7d'" :utilization="w.used_percent" :resets-at="date(w.reset_at)" :color="w.limit_window_seconds===18000?'indigo':'emerald'"/><span v-for="seconds in missingWindows" :key="seconds" class="block text-gray-400">{{seconds===18000?'5h':'7d'}}：未提供</span><span v-if="!windows.length" class="text-gray-400">待查询</span><div v-if="checkedAt" class="text-gray-400">{{checkedAt}}</div><details v-if="error||usage?.error" class="max-w-56 whitespace-normal text-amber-600"><summary>额度查询失败，保留上次数据</summary>{{error||usage.error}}</details></div></template>
