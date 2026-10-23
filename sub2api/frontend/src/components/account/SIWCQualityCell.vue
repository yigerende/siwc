<script setup lang="ts">
import {computed,ref} from 'vue'
import {apiClient} from '@/api/client'
const props=defineProps<{account:any}>()
const result=computed(()=>props.account.extra?.siwc_quality)
const labels:Record<string,string>={normal:'正常',degraded:'降智',suspect:'待确认',error:'检测失败',unknown:'待检测',stale:'已过期'}
const busy=ref(false),error=ref(''),history=ref<any[]|null>(null)
const status=computed(()=>result.value?.overall?.status||'unknown')
async function run(){busy.value=true;error.value='';try{await apiClient.post('/admin/account-quality/run-selected',{account_ids:[props.account.id]})}catch(e:any){error.value=e.message}finally{busy.value=false}}
async function load(){try{history.value=(await apiClient.get(`/admin/account-quality/accounts/${props.account.id}/history`)).data.items}catch(e:any){error.value=e.message}}
</script>
<template><div v-if="account.credentials?.auth_mode==='siwc'||result" class="min-w-28 text-xs"><strong :class="status==='normal'?'text-green-600':status==='degraded'?'text-red-600':'text-gray-500'">{{labels[status]||status}}</strong><div v-if="result?.question?.checked_at" class="text-gray-400">{{new Date(result.question.checked_at).toLocaleString()}}</div><details class="max-w-64 whitespace-normal"><summary class="cursor-pointer text-primary-500">查看检测</summary><p>答题：{{labels[result?.question?.status]||'待检测'}} · 连续失败 {{result?.question?.failures||0}} 次</p><p>{{result?.question?.answer}} · {{result?.question?.duration_ms||0}} ms</p><p>模型：{{result?.model?.sent_model||'—'}} → {{result?.model?.response_model||'—'}}</p><p>{{result?.question?.error||result?.model?.error}}</p><button class="mr-3 text-primary-500" :disabled="busy" @click="run">{{busy?'已提交…':'立即检测'}}</button><button class="text-primary-500" @click="load">历史记录</button><div v-for="v in history" :key="v.version" class="mt-2 border-t pt-1">{{new Date(v.recorded_at).toLocaleString()}} · {{labels[v.overall?.status]||v.overall?.status}}<p>{{v.question?.answer}} · {{v.question?.duration_ms}} ms</p></div></details><p v-if="error" class="text-red-600">{{error}}</p></div><span v-else class="text-gray-400">—</span></template>
