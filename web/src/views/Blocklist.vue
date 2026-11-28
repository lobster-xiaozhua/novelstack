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

function stateOf(e) {
  if (e.active) return { cls: 'on', text: '封禁中' }
  if (e.unblocked_at) return { cls: 'off', text: '已解封' }
  return { cls: 'off', text: '已到期' }
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
          <b :class="enabled ? 'ok' : 'dim'">{{ enabled ? '已开启' : '已停用' }}</b>
          · 当前封禁 {{ activeCount }} 个 IP
        </div>
      </div>
      <div class="actions">
        <button class="btn ghost" :disabled="loading" @click="load">刷新</button>
      </div>
    </header>

    <div v-if="error" class="error">{{ error }}</div>

    <section class="card">
      <div v-if="loading && shown.length === 0" class="loading">正在读取…</div>
      <div v-else-if="shown.length === 0" class="empty">
        暂无记录。熔断触发后，来源 IP 会出现在这里。
      </div>
      <table v-else class="tbl">
        <thead>
          <tr>
            <th>IP</th>
            <th>状态</th>
            <th class="num">触发</th>
            <th>原因</th>
            <th>封禁时间</th>
            <th>到期 / 解封</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="e in shown" :key="e.ip">
            <td class="mono" data-label="IP">{{ e.ip }}</td>
            <td data-label="状态">
              <span class="pill" :class="stateOf(e).cls">{{ stateOf(e).text }}</span>
            </td>
            <td class="num" data-label="触发">{{ e.hits }} 次</td>
            <td data-label="原因">{{ e.reason || '—' }}</td>
            <td class="dim" data-label="封禁时间">{{ fmt(e.created_at) }}</td>
            <td data-label="到期 / 解封">
              <template v-if="e.active">
                <span class="warn">{{ remain(e.expires_at) }}</span>
              </template>
              <template v-else>{{ fmt(e.unblocked_at || e.expires_at) }}</template>
            </td>
            <td data-label="操作">
              <button
                v-if="e.active"
                class="btn ghost"
                :disabled="busy === e.ip"
                @click="doUnblock(e.ip)"
              >{{ busy === e.ip ? '处理中…' : '解封' }}</button>
              <span v-else class="dim">—</span>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <p class="note">
      解封操作会写入审计日志（action=ip_unblocked）。回环地址（127.0.0.1 / ::1）永不封禁，避免本机自测互相污染。
    </p>
  </div>
</template>

<style scoped>
.ok { color: var(--green); }
.dim { color: var(--secondary); }
.warn { color: var(--orange); font-variant-numeric: tabular-nums; }
.note { margin-top: 14px; line-height: 1.7; }
</style>
