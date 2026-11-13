<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { siwcAPI } from '@/api/admin/siwc'

const enabled = ref(false)
const loaded = ref(false)
const busy = ref(true)
const error = ref('')

async function load() {
  busy.value = true
  error.value = ''
  try {
    enabled.value = (await siwcAPI.remoteCompaction()).enabled
    loaded.value = true
  } catch {
    error.value = '设置读取失败，点击重试'
  } finally {
    busy.value = false
  }
}

async function toggle() {
  if (busy.value || !loaded.value) return
  busy.value = true
  error.value = ''
  try {
    enabled.value = (await siwcAPI.saveRemoteCompaction(!enabled.value)).enabled
  } catch {
    // A timeout may happen after the save committed. Re-read before allowing
    // another toggle instead of displaying an assumed server state.
    loaded.value = false
    error.value = '保存结果未确认，点击重新读取'
  } finally {
    busy.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="flex flex-col items-start gap-1">
    <button
      type="button"
      role="switch"
      :aria-checked="enabled"
      aria-label="SIWC 远程压缩兼容"
      :aria-busy="busy"
      :disabled="busy || !loaded"
      class="btn btn-secondary gap-2"
      title="开启后兼容 SIWC 客户端远程压缩请求；默认关闭。普通 OAuth 不受此开关影响。"
      @click="toggle"
    >
      <span
        class="relative inline-flex h-5 w-9 shrink-0 rounded-full transition-colors"
        :class="enabled && loaded ? 'bg-primary-600' : 'bg-gray-300 dark:bg-dark-500'"
        aria-hidden="true"
      >
        <span class="absolute top-0.5 h-4 w-4 rounded-full bg-white transition-transform" :class="enabled && loaded ? 'translate-x-4' : 'translate-x-0.5'" />
      </span>
      <span>SIWC 远程压缩兼容</span>
      <span class="text-xs">{{ busy ? '处理中…' : !loaded ? '状态未确认' : enabled ? '已开启' : '已关闭' }}</span>
    </button>
    <button v-if="error" type="button" class="text-xs text-red-600 dark:text-red-400" :disabled="busy" role="alert" @click="load">{{ error }}</button>
  </div>
</template>
