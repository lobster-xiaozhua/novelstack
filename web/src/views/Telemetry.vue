<script setup>
import { computed, onMounted, ref } from 'vue'
import { ms, ts } from '../format'
import {
  getTelemetryOverview,
  getTelemetryHeatmap,
  getTelemetrySlowChapters,
  getTelemetryCrashes,
} from '../api'

const days = ref(7)
const loading = ref(false)
const error = ref('')
const overview = ref(null)
const cells = ref([])
const slow = ref([])
const crashes = ref([])
const expanded = ref(new Set())

async function load() {
  loading.value = true
  error.value = ''
  try {
    const d = days.value
    const [o, h, s, c] = await Promise.all([
      getTelemetryOverview(d),
      getTelemetryHeatmap(d),
      getTelemetrySlowChapters(d),
      getTelemetryCrashes(d, 50),
    ])
    overview.value = o
    cells.value = h.cells || []
    slow.value = s.items || []
    crashes.value = c.items || []
  } catch (e) {
    error.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

onMounted(load)

const models = computed(() => {
  const set = []
  for (const c of cells.value) if (!set.includes(c.model)) set.push(c.model)
  return set.slice(0, 10)
})

const kinds = computed(() => {
  const set = []
  for (const c of cells.value) if (!set.includes(c.kind)) set.push(c.kind)
  return set.slice(0, 8)
})

function cellCount(model, kind) {
  const hit = cells.value.find((c) => c.model === model && c.kind === kind)
  return hit ? hit.count : 0
}

const maxCell = computed(() => Math.max(1, ...cells.value.map((c) => c.count)))

// 用透明度表达量级：0 值留空，其余按比例加深，避免固定色阶掩盖长尾。
function cellStyle(model, kind) {
  const n = cellCount(model, kind)
  if (!n) return {}
  const a = 0.12 + 0.62 * (n / maxCell.value)
  return { background: `rgba(226, 109, 117, ${a.toFixed(3)})` }
}

function toggle(i) {
  const next = new Set(expanded.value)
  next.has(i) ? next.delete(i) : next.add(i)
  expanded.value = next
}

function firstLine(name) {
  return (name || '(anonymous)').split('\n')[0]
}
</script>

<template>
  <div>
    <header class="top">
      <div>
        <h1>体验调优</h1>
        <div class="sub">
          客户端遥测 · 只含运行状况与耗时，不含正文、音频与凭证
        </div>
      </div>
      <div class="actions">
        <select v-model.number="days" class="btn" @change="load">
          <option :value="7">最近 7 天</option>
          <option :value="14">最近 14 天</option>
          <option :value="30">最近 30 天</option>
        </select>
        <button class="btn" :disabled="loading" @click="load">刷新</button>
      </div>
    </header>

    <div v-if="error" class="error">{{ error }}</div>

    <section v-if="overview" class="grid cols-4">
      <div class="card kpi">
        <div class="label">活跃设备</div>
        <div class="value">{{ overview.active_devices }}</div>
        <div class="foot">上报过遥测的去重设备</div>
      </div>
      <div class="card kpi">
        <div class="label">崩溃 / ANR</div>
        <div class="value" :class="{ 'bad-text': overview.crashes > 0 }">{{ overview.crashes }}</div>
        <div class="foot">按 occurrence 合并计数</div>
      </div>
      <div class="card kpi">
        <div class="label">API P95</div>
        <div class="value">{{ ms(overview.api_p95_ms) }}</div>
        <div class="foot">接口端到端耗时</div>
      </div>
      <div class="card kpi">
        <div class="label">冷启动 P95</div>
        <div class="value">{{ ms(overview.cold_start_p95_ms) }}</div>
        <div class="foot">进程启动到首帧</div>
      </div>
    </section>

    <section class="card" style="margin-top: 14px">
      <h2>机型热力图 <span class="hint">行=机型，列=事件类型，颜色越深次数越多</span></h2>
      <div v-if="!models.length" class="loading">暂无数据（客户端开启 WiFi 后会自动上传）</div>
      <div v-else class="heat-wrap">
        <table class="heat">
          <thead>
            <tr>
              <th></th>
              <th v-for="k in kinds" :key="k">{{ k }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="m in models" :key="m">
              <th class="model">{{ m }}</th>
              <td v-for="k in kinds" :key="k" :style="cellStyle(m, k)" :title="`${m} / ${k}: ${cellCount(m, k)}`">
                {{ cellCount(m, k) || '' }}
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </section>

    <section class="grid cols-2" style="margin-top: 14px">
      <div class="card">
        <h2>最慢章节 <span class="hint">解密+渲染耗时，含 P95</span></h2>
        <div v-if="!slow.length" class="loading">暂无慢章节记录</div>
        <table v-else class="tbl">
          <thead>
            <tr><th>章节</th><th class="num">次数</th><th class="num">均值</th><th class="num">P95</th><th class="num">峰值</th></tr>
          </thead>
          <tbody>
            <tr v-for="s in slow" :key="s.name">
              <td class="ellip" data-label="章节">{{ s.name || '(未命名)' }}</td>
              <td class="num" data-label="次数">{{ s.count }}</td>
              <td class="num" data-label="均值">{{ ms(s.avg_ms) }}</td>
              <td class="num" data-label="P95">{{ ms(s.p95_ms) }}</td>
              <td class="num" data-label="峰值">{{ ms(s.max_ms) }}</td>
            </tr>
          </tbody>
        </table>
      </div>

      <div class="card">
        <h2>崩溃与 ANR <span class="hint">点击展开完整堆栈（已截断 100 行）</span></h2>
        <div v-if="!crashes.length" class="loading">没有崩溃记录，保持住</div>
        <div v-else class="crash-list">
          <div v-for="(c, i) in crashes" :key="i" class="crash">
            <button type="button" class="crash-head" @click="toggle(i)">
              <span class="pill bad">×{{ c.occurrence }}</span>
              <span class="crash-name">{{ firstLine(c.name) }}</span>
              <span class="crash-meta">{{ c.model }} · v{{ c.app_version }} · {{ ts(c.event_time) }}</span>
            </button>
            <pre v-if="expanded.has(i)" class="crash-body">{{ c.stack || '(无堆栈)' }}
---
{{ JSON.stringify(c.context || {}, null, 2) }}</pre>
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.bad-text { color: var(--bad); }
.heat-wrap { overflow-x: auto; }
.heat { border-collapse: separate; border-spacing: 3px; font-size: 11.5px; width: 100%; }
.heat th { font-weight: 500; color: var(--fg-faint); text-align: center; padding: 4px 6px; white-space: nowrap; }
.heat th.model { text-align: left; letter-spacing: 0; text-transform: none; color: var(--fg); }
.heat td {
  text-align: center; padding: 7px 8px; border-radius: 4px;
  background: rgba(255, 255, 255, .03); min-width: 54px;
  font-family: var(--mono); font-variant-numeric: tabular-nums;
}
.crash-list { display: flex; flex-direction: column; gap: 6px; max-height: 420px; overflow-y: auto; }
.crash { border: 1px solid var(--line); border-radius: 6px; overflow: hidden; }
.crash-head {
  display: flex; align-items: center; gap: 8px; width: 100%; text-align: left;
  background: transparent; border: 0; color: var(--fg); padding: 9px 10px; cursor: pointer;
}
.crash-head:hover { background: rgba(255, 255, 255, .03); }
.crash-name { flex: 1; font-size: 12.5px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.crash-meta { color: var(--fg-faint); font-size: 11px; white-space: nowrap; }
.crash-body {
  margin: 0; padding: 10px 12px; border-top: 1px solid var(--line);
  font-size: 11px; line-height: 1.55; color: var(--fg-dim);
  background: rgba(0, 0, 0, .22); white-space: pre-wrap; word-break: break-all; max-height: 320px; overflow: auto;
}
</style>
