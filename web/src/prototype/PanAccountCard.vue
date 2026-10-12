<script setup lang="ts">
import { computed } from 'vue'
import { Cloud, RefreshCw } from 'lucide-vue-next'

const props = defineProps<{
  account: { username: string; userID: number; used: number; total: number; remain: number; vip: string; expire: string; ready: boolean; updated: string }
  connected: boolean | null
  loading: boolean
  error: string
}>()
defineEmits<{ refresh: [] }>()

const status = computed(() => props.connected === true ? '已连接' : props.connected === false ? '连接异常' : props.loading ? '正在检查' : '状态未读取')
const usage = computed(() => props.account.total > 0 ? Math.min(100, Math.round(props.account.used / props.account.total * 100)) : 0)
const updated = computed(() => props.account.updated ? new Date(props.account.updated).toLocaleString('zh-CN', { hour12: false }) : '')
const space = (value: number) => props.account.ready ? `${value.toFixed(2)} TB` : '待读取'
</script>

<template>
  <article class="board-panel pan-account-card">
    <header>
      <div class="pan-account-heading"><Cloud :size="20" /><h2>115 网盘状态</h2><span class="board-status" :class="{ success: connected === true, danger: connected === false, warn: connected === null }">{{ status }}</span></div>
      <button class="board-button quiet tiny" :disabled="loading" @click="$emit('refresh')"><RefreshCw :size="15" :class="{ spin: loading }" />{{ loading ? '读取中' : '刷新状态' }}</button>
    </header>
    <div class="pan-account-details">
      <div><span>账号</span><strong>{{ account.username || (account.userID ? `115 用户 ${account.userID}` : '待读取') }}</strong><small v-if="account.vip">{{ account.vip }}</small></div>
      <div><span>网盘总容量</span><strong>{{ space(account.total) }}</strong></div>
      <div><span>已用空间</span><strong>{{ space(account.used) }}</strong><small v-if="account.ready">{{ usage }}%</small></div>
      <div><span>剩余空间</span><strong class="pan-account-remain">{{ space(account.remain) }}</strong></div>
    </div>
    <div v-if="account.ready" class="pan-account-meter" role="progressbar" aria-label="115 网盘空间使用率" :aria-valuenow="usage" aria-valuemin="0" aria-valuemax="100"><i :style="{ width: `${usage}%` }"></i></div>
    <p v-if="error" class="pan-account-error" role="status">账号信息读取失败：{{ error }}。{{ account.ready ? '当前保留上次读取的数据，请刷新重试。' : '请刷新重试。' }}</p>
    <footer v-if="account.expire || updated"><span v-if="account.expire">会员到期：{{ account.expire }}</span><span v-if="updated">信息更新：{{ updated }}</span></footer>
  </article>
</template>

<style scoped>
.pan-account-card { margin-bottom: 20px; }
.pan-account-heading { display: flex; align-items: center; flex-wrap: wrap; gap: 10px; }
.pan-account-heading > svg { color: var(--board-brand); }
.pan-account-heading h2 { margin: 0; }
.pan-account-card > header > button { flex-shrink: 0; white-space: nowrap; }
.pan-account-details { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 18px; margin: 22px 0 16px; }
.pan-account-details > div { display: flex; align-items: flex-start; flex-direction: column; gap: 7px; }
.pan-account-details span, .pan-account-details small, footer { color: var(--board-muted); font-size: 12px; }
.pan-account-details strong { font-size: 19px; overflow-wrap: anywhere; }
.pan-account-details .pan-account-remain { color: var(--board-brand); }
.pan-account-meter { height: 5px; overflow: hidden; background: var(--board-surface-2); border-radius: 5px; }
.pan-account-meter i { display: block; height: 100%; background: var(--board-brand); }
.pan-account-error { margin: 14px 0 0; color: #e6ab71; font-size: 13px; line-height: 1.6; overflow-wrap: anywhere; }
footer { display: flex; justify-content: space-between; flex-wrap: wrap; gap: 10px; margin-top: 14px; }
@media (max-width: 600px) { .pan-account-details { grid-template-columns: repeat(2, minmax(0, 1fr)); } .pan-account-details strong { font-size: 17px; } }
</style>
