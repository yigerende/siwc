<script setup lang="ts">
import { onMounted, ref } from 'vue'
import api from '@/api/client'

const cfg = ref({ enabled: false, retain_minutes: 60, interval_minutes: 5 })
const savedEnabled = ref(false)
const busy = ref(false), loaded = ref(false), error = ref(''), message = ref('')
async function load() {
  busy.value = true; error.value = ''; message.value = ''
  try { cfg.value = (await api.get('/admin/siwc/space-cleanup')).data; loaded.value = true; savedEnabled.value = cfg.value.enabled }
  catch { loaded.value = false; error.value = '读取失败，请重新读取' }
  finally { busy.value = false }
}
async function save() {
  if (!loaded.value || busy.value) return
  if (!Number.isInteger(cfg.value.retain_minutes) || cfg.value.retain_minutes < 0 || cfg.value.retain_minutes > 43200 || !Number.isInteger(cfg.value.interval_minutes) || cfg.value.interval_minutes < 1 || cfg.value.interval_minutes > 1440) { error.value = '请填写有效的整数分钟'; return }
  busy.value = true; error.value = ''; message.value = ''
  try { cfg.value = (await api.put('/admin/siwc/space-cleanup', cfg.value)).data; savedEnabled.value = cfg.value.enabled; message.value = '已保存' }
  catch { loaded.value = false; error.value = '保存结果未确认，请重新读取' }
  finally { busy.value = false }
}
onMounted(load)
</script>

<template>
  <details class="relative max-w-full">
    <summary class="btn btn-secondary cursor-pointer">Space 已完成自动删除 · {{ !loaded ? (busy ? '读取中' : '状态未确认') : savedEnabled ? '已开启' : '已关闭' }}</summary>
    <form class="mt-2 flex max-w-md flex-col gap-3 rounded-lg border border-gray-200 bg-white p-3 text-sm dark:border-dark-600 dark:bg-dark-800" @submit.prevent="save">
      <label class="flex items-center gap-2"><input v-model="cfg.enabled" type="checkbox" :disabled="busy || !loaded" />启用自动删除</label>
      <label class="flex flex-wrap items-center gap-2">完成后保留<input v-model.number="cfg.retain_minutes" aria-label="完成后保留分钟" type="number" min="0" max="43200" step="1" class="input w-24" :disabled="busy || !loaded" />分钟</label>
      <label class="flex flex-wrap items-center gap-2">每隔<input v-model.number="cfg.interval_minutes" aria-label="清理间隔分钟" type="number" min="1" max="1440" step="1" class="input w-24" :disabled="busy || !loaded" />分钟清理</label>
      <p class="text-xs text-gray-500 dark:text-gray-400">仅删除仍标记“Space 已完成”的 SIWC 账号。0 分钟表示下次清理即删除；每轮最多 100 个。历史请求和计费记录保留，不会删除 Space 中的账号。</p>
      <p v-if="error" role="alert" class="text-xs text-red-600">{{ error }}</p>
      <p v-if="message" role="status" class="text-xs text-emerald-600">{{ message }}</p>
      <div class="flex gap-2"><button class="btn btn-primary" type="submit" :disabled="busy || !loaded">{{ busy ? '处理中…' : '保存' }}</button><button class="btn btn-secondary" type="button" :disabled="busy" @click="load">重新读取</button></div>
    </form>
  </details>
</template>
