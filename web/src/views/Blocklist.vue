<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { getBlocklist, unblockIP } from '../api'

const entries = ref([])
const enabled = ref(true)
const loading = ref(true)
const error = ref('')
const busy = ref('')
const now = ref(Date.now())
let timer = null

const activeCount = computed(() => entries.value.filter((e) => e.active).length)
const shown = computed(() => entries.value)

function fmt(iso) {
  if (!iso) return '—'
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString('zh-CN', { hour12: false })
}

function remain(iso) {
  if (!iso) return ''
  const ms = new Date(iso).getTime() - now.value
  if (Number.isNaN(ms)) return ''
  if (ms <= 0) return '已到期'
  const m = Math.floor(ms / 60000)
  if (m < 60) return `剩 ${m} 分`
  const h = Math.floor(m / 60)
  return `剩 ${h} 小时 ${m % 60} 分`
}

async function load() {
  loading.value = true
  error.value = ''
  try {
    const r = await getBlocklist()
    entries.value = r.entries || []
    enabled.value = !!r.enabled
  } catch (e) {
    error.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

async function doUnblock(ip) {
  busy.value = ip
  error.value = ''
  try {
    await unblockIP(ip)
    await load()
  } catch (e) {
    error.value = e.message || String(e)
  } finally {
    busy.value = ''
  }
}

onMounted(() => {
  load()
  timer = setInterval(() => { now.value = Date.now(); load() }, 15000)
})
onUnmounted(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div>
    <header class="top">
      <div>
        <h1>IP 防护 · 黑名单</h1>
        <div class="sub">
          1 小时内累计 10 次 401/404 自动封禁 24 小时 · 状态：
          <b :class="enabled ? 'ok' : 'off'">{{ enabled ? '已开启' : '已停用' }}</b>
          · 当前封禁 {{ activeCount }} 个 IP
        </div>
      </div>
      <button class="btn ghost" :disabled="loading" @click="load">刷新</button>
    </header>

    <div v-if="error" class="alert">{{ error }}</div>

    <section class="card">
      <div class="empty" v-if="!loading && shown.length === 0">
        暂无记录。熔断触发后，来源 IP 会出现在这里。
      </div>
      <div v-else class="list">
        <div class="row" v-for="e in shown" :key="e.ip">
          <div class="ip-col">
            <code>{{ e.ip }}</code>
            <span class="tag" :class="e.active ? 'on' : 'off'">{{ e.active ? '封禁中' : (e.unblocked_at ? '已解封' : '已到期') }}</span>
          </div>
          <div class="meta">
            <span>触发 {{ e.hits }} 次</span>
            <span>原因 {{ e.reason }}</span>
            <span>{{ fmt(e.created_at) }}</span>
            <span v-if="e.active" class="remain">{{ remain(e.expires_at) }}</span>
            <span v-else>解封 {{ fmt(e.unblocked_at || e.expires_at) }}</span>
          </div>
          <button
            v-if="e.active"
            class="btn ghost"
            :disabled="busy === e.ip"
            @click="doUnblock(e.ip)"
          >{{ busy === e.ip ? '处理中…' : '解封' }}</button>
        </div>
      </div>
    </section>

    <p class="note">
      解封操作会写入审计日志（action=ip_unblocked）。回环地址（127.0.0.1 / ::1）永不封禁，避免本机自测互相污染。
    </p>
  </div>
</template>

<style scoped>
.ok { color: #16a34a; }
.off { color: #94a3b8; }
.alert { color: #fca5a5; background: #7f1d1d33; padding: 10px 14px; border-radius: 8px; margin-bottom: 12px; }
.card { background: #ffffff08; border: 1px solid #ffffff14; border-radius: 12px; padding: 16px; }
.empty { color: #94a3b8; padding: 32px; text-align: center; }
.list { display: flex; flex-direction: column; }
.row { display: flex; align-items: center; gap: 12px; padding: 12px 4px; border-bottom: 1px solid #ffffff10; }
.row:last-child { border-bottom: 0; }
.ip-col { min-width: 210px; display: flex; align-items: center; gap: 8px; }
.ip-col code { color: #93c5fd; font-size: 13px; }
.tag { font-size: 11px; padding: 2px 7px; border-radius: 999px; }
.tag.on { background: #7f1d1d; color: #fecaca; }
.tag.off { background: #ffffff14; color: #94a3b8; }
.meta { flex: 1; display: flex; flex-wrap: wrap; gap: 14px; color: #cbd5e1; font-size: 12px; }
.remain { color: #fbbf24; }
.note { color: #94a3b8; font-size: 12px; margin-top: 12px; line-height: 1.7; }
</style>
