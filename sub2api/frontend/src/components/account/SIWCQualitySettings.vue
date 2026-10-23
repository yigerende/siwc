<script setup lang="ts">
import {ref} from 'vue'
import {apiClient} from '@/api/client'
const open=ref(false),busy=ref(false),message=ref(''),form=ref<any>(null),groups=ref<any[]>([])
async function show(){open.value=true;message.value='';try{form.value=(await apiClient.get('/admin/account-quality/settings')).data.settings;const r=(await apiClient.get('/admin/groups/all',{params:{platform:'openai'}})).data;groups.value=Array.isArray(r)?r:r.items||[]}catch(e:any){message.value=e.message}}
async function save(){busy.value=true;try{form.value.degradation_conditions=[...(form.value.question_enabled?['question']:[]),...(form.value.model_audit_enabled?['model']:[])];form.value=(await apiClient.put('/admin/account-quality/settings',form.value)).data.settings;message.value='设置已保存'}catch(e:any){message.value=e.message}finally{busy.value=false}}
async function run(){busy.value=true;try{await apiClient.post('/admin/account-quality/run',{});message.value='已开始检测，结果会显示在智商情况列'}catch(e:any){message.value=e.message}finally{busy.value=false}}
function addQuestion(){form.value.questions.push({id:crypto.randomUUID(),name:'新题目',enabled:true,prompt:'',answer:'',match_mode:'answer',max_duration_ms:20000})}
</script>
<template>
 <button class="btn btn-secondary" @click="show">SIWC 降智检测</button>
 <Teleport to="body"><div v-if="open" class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-5" @click.self="open=false"><section class="max-h-[90vh] w-full max-w-3xl overflow-auto rounded-xl bg-white p-6 shadow-lg dark:bg-dark-800"><div class="mb-4 flex justify-between"><h2 class="text-lg font-semibold">SIWC 降智检测</h2><button @click="open=false">关闭</button></div><p class="mb-4 text-sm text-gray-500">通过 SIWC 请求检测答题和实际模型。结果供 Space 读取，由 Space 按配置处理账号。</p><p v-if="message" role="status" class="mb-3 text-sm">{{message}}</p>
 <form v-if="form" @submit.prevent="save" class="space-y-5">
  <div class="flex flex-wrap gap-5"><label><input type="checkbox" v-model="form.enabled"/> 启用定时检测</label><label><input type="checkbox" v-model="form.question_enabled"/> 答题检测</label><label><input type="checkbox" v-model="form.model_audit_enabled"/> 模型检测</label></div>
  <label class="block"><input type="checkbox" v-model="form.all_groups"/> 检测全部分组</label><div v-if="!form.all_groups" class="flex flex-wrap gap-3"><label v-for="g in groups" :key="g.id"><input type="checkbox" v-model="form.group_ids" :value="g.id"/> {{g.name}}</label></div>
  <div class="grid grid-cols-2 gap-4">
   <label>综合降智条件<select class="input mt-1" v-model="form.degradation_mode"><option value="any">任一启用项异常</option><option value="all">全部启用项异常</option></select></label>
   <label>答题模型<input class="input mt-1" v-model="form.model" required/></label><label>模型检测请求模型<input class="input mt-1" v-model="form.model_audit_model" required/></label>
   <label>判断方式<select class="input mt-1" v-model="form.mode"><option value="content_time">内容与耗时</option><option value="content">内容</option><option value="time">耗时</option></select></label>
   <label>推理强度<select class="input mt-1" v-model="form.reasoning_effort"><option v-for="v in ['low','medium','high','xhigh']" :key="v">{{v}}</option></select></label>
   <label v-for="[key,label,min,max] in [['interval_seconds','答题周期（秒）',10,86400],['model_audit_interval_seconds','模型检测周期（秒）',10,86400],['retry_seconds','失败重试间隔（秒）',10,86400],['failure_limit','连续异常次数',1,20],['recovery_limit','连续恢复次数',1,20],['concurrency','检测并发',1,8],['timeout_seconds','请求超时（秒）',5,300],['history_limit','每账号保留记录数',1,1000]]" :key="String(key)">{{label}}<input class="input mt-1" type="number" v-model.number="form[String(key)]" :min="Number(min)" :max="Number(max)" required/></label>
  </div>
  <div class="flex items-center justify-between"><strong>检测题库</strong><button type="button" class="btn btn-secondary" @click="addQuestion">增加题目</button></div>
  <article v-for="(q,index) in form.questions" :key="q.id" class="space-y-3 rounded border p-3 dark:border-dark-600"><div class="flex gap-3"><input type="checkbox" v-model="q.enabled"/><input class="input" v-model="q.name" required/><button type="button" @click="form.questions.splice(index,1)">移除</button></div><textarea class="input" rows="3" v-model="q.prompt" required/><div class="grid grid-cols-3 gap-3"><label>答案<input class="input" v-model="q.answer"/></label><label>匹配<select class="input" v-model="q.match_mode"><option value="answer">标准答案</option><option value="keyword">关键词</option><option value="regex">正则</option></select></label><label>最长耗时（ms）<input class="input" type="number" min="1" max="300000" v-model.number="q.max_duration_ms"/></label></div></article>
  <div class="flex gap-3"><button class="btn btn-primary" :disabled="busy">保存设置</button><button class="btn btn-secondary" type="button" :disabled="busy||!form.enabled" @click="run">立即检测</button></div>
 </form></section></div></Teleport>
</template>
