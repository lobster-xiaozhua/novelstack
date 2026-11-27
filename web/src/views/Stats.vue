<script setup>
import { computed, onMounted, ref } from 'vue'
import { getReadingStats, getExportPreview, downloadExport } from '../api'

const loading = ref(false)
const error = ref('')
const stats = ref(null)

const preview = ref(null)
const selected = ref([])
const format = ref('txt')
const password = ref('')
const exporting = ref(false)
const exportMsg = ref('')
const exportErr = ref('')

const WEEKDAYS = ['一', '二', '三', '四', '五', '六', '日']

async function load() {
  loading.value = true
  error.value = ''
  try {
    stats.value = await getReadingStats()
  } catch (e) {
    error.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

async function loadPreview() {
  try {
    preview.value = await getExportPreview()
  } catch (e) {
    exportErr.value = e.message || String(e)
  }
}

onMounted(() => {
  load()
  loadPreview()
})

// ---- 时长格式化 ----

function human(seconds) {
  const s = Math.max(0, Math.round(seconds || 0))
  if (s < 60) return `${s} 秒`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} 分钟`
  const h = Math.floor(m / 60)
  const rest = m % 60
  return rest ? `${h} 小时 ${rest} 分` : `${h} 小时`
}

// ---- 柱状图：最近 30 天 ----

const barDays = computed(() => (stats.value?.days || []).slice(-30))

const barMax = computed(() => Math.max(1, ...barDays.value.map((d) => d.seconds)))

function barHeight(d) {
  if (!d.seconds) return '2px'
  const pct = Math.max(6, Math.round((d.seconds / barMax.value) * 100))
  return `${pct}%`
}

function shortDay(day) {
  const [, m, d] = day.split('-')
  return `${Number(m)}/${Number(d)}`
}

// ---- 热力图：最近 90 天，按周分列（周一为每列第一行） ----

const weeks = computed(() => {
  const list = stats.value?.days || []
  if (!list.length) return []
  const first = new Date(`${list[0].day}T00:00:00`)
  const pad = (first.getDay() + 6) % 7
  const cells = Array.from({ length: pad }, () => null).concat(list)
  const out = []
  for (let i = 0; i < cells.length; i += 7) {
    const col = cells.slice(i, i + 7)
    while (col.length < 7) col.push(null)
    out.push(col)
  }
  return out
})

const heatMax = computed(() => Math.max(1, ...(stats.value?.days || []).map((d) => d.seconds)))

function heatLevel(seconds) {
  if (!seconds) return 0
  const r = seconds / heatMax.value
  if (r > 0.75) return 4
  if (r > 0.5) return 3
  if (r > 0.25) return 2
  return 1
}

function heatTitle(cell) {
  if (!cell) return ''
  return `${cell.day} · ${human(cell.seconds)}`
}

// ---- 导出 ----

const allSelected = computed(
  () => !!preview.value && selected.value.length === preview.value.books.length,
)

function toggleAll() {
  if (!preview.value) return
  selected.value = allSelected.value ? [] : preview.value.books.map((b) => b.id)
}

function toggleOne(id) {
  const i = selected.value.indexOf(id)
  if (i >= 0) selected.value.splice(i, 1)
  else selected.value.push(id)
}

async function runExport() {
  exportErr.value = ''
  exportMsg.value = ''
  if (!password.value) {
    exportErr.value = '请输入当前管理员密码'
    return
  }
  exporting.value = true
  try {
    const size = await downloadExport({
      password: password.value,
      bookIds: selected.value,
      format: format.value,
    })
    exportMsg.value = `已生成 ${(size / 1024 / 1024).toFixed(2)} MB 的导出包`
    password.value = ''
  } catch (e) {
    exportErr.value = e.message || String(e)
  } finally {
    exporting.value = false
  }
}
</script>

<template>
  <div>
    <header class="top">
      <div>
        <h1>阅读统计</h1>
        <div class="sub">
          按服务端时区自然日统计 · 只含时长与天数，不含书名与正文
        </div>
      </div>
      <div class="actions">
        <button class="btn" :disabled="loading" @click="load">刷新</button>
      </div>
    </header>

    <div v-if="error" class="error">{{ error }}</div>

    <section v-if="stats" class="grid cols-4">
      <div class="card kpi">
        <div class="label">今日阅读</div>
        <div class="value">{{ human(stats.today_seconds) }}</div>
        <div class="foot">跨零点自动归入新的一天</div>
      </div>
      <div class="card kpi">
        <div class="label">累计阅读</div>
        <div class="value">{{ human(stats.total_seconds) }}</div>
        <div class="foot">共 {{ stats.total_days }} 天有记录</div>
      </div>
      <div class="card kpi">
        <div class="label">连续阅读</div>
        <div class="value">{{ stats.streak_days }} <span class="unit">天</span></div>
        <div class="foot">今天还没读不算断签</div>
      </div>
      <div class="card kpi">
        <div class="label">最长连续</div>
        <div class="value">{{ stats.best_streak }} <span class="unit">天</span></div>
        <div class="foot">历史最佳</div>
      </div>
    </section>

    <section class="card" style="margin-top: 14px">
      <h2>最近 30 天 <span class="hint">柱高按当天时长相对最大值</span></h2>
      <div v-if="!barDays.length" class="loading">暂无数据</div>
      <div v-else class="bars">
        <div v-for="d in barDays" :key="d.day" class="bar-col" :title="`${d.day} · ${human(d.seconds)}`">
          <div class="bar-track">
            <div class="bar" :class="{ zero: !d.seconds }" :style="{ height: barHeight(d) }" />
          </div>
          <div class="bar-label">{{ shortDay(d.day) }}</div>
        </div>
      </div>
    </section>

    <section class="card" style="margin-top: 14px">
      <h2>阅读日历 <span class="hint">最近 90 天 · 颜色越深阅读越久</span></h2>
      <div v-if="!weeks.length" class="loading">暂无数据</div>
      <div v-else class="heat">
        <div class="heat-weekdays">
          <span v-for="w in WEEKDAYS" :key="w">{{ w }}</span>
        </div>
        <div class="heat-grid">
          <div v-for="(col, ci) in weeks" :key="ci" class="heat-col">
            <div
              v-for="(cell, ri) in col"
              :key="ri"
              class="heat-cell"
              :class="cell ? `lv${heatLevel(cell.seconds)}` : 'empty'"
              :title="heatTitle(cell)"
            />
          </div>
        </div>
      </div>
    </section>

    <section class="card" style="margin-top: 14px">
      <h2>数据导出 <span class="hint">不再使用本系统时的退路 · 需再次输入密码</span></h2>
      <p class="warn">
        导出的是<strong>解密后的明文</strong>（已应用正文净化规则），请把 ZIP 保存到可信位置，
        并避免通过公共网络传输。
      </p>

      <div v-if="preview" class="export-grid">
        <div class="export-books">
          <label class="pick-all">
            <input type="checkbox" :checked="allSelected" @change="toggleAll" />
            全选（{{ selected.length }} / {{ preview.books.length }}）
          </label>
          <div class="book-list">
            <label v-for="b in preview.books" :key="b.id" class="book-item">
              <input
                type="checkbox"
                :checked="selected.includes(b.id)"
                @change="toggleOne(b.id)"
              />
              <span class="book-title">{{ b.title || '(未命名)' }}</span>
              <span class="book-meta">{{ b.chapter_count }} 章</span>
            </label>
            <div v-if="!preview.books.length" class="loading">书库为空</div>
          </div>
          <div class="hint-line">不勾选任何书 = 导出全部（上限 {{ preview.max_books }} 本）</div>
        </div>

        <div class="export-form">
          <label class="field">
            <span>格式</span>
            <select v-model="format" class="btn">
              <option value="txt">TXT（纯文本）</option>
              <option value="epub">EPUB（带目录）</option>
            </select>
          </label>
          <label class="field">
            <span>当前管理员密码</span>
            <input v-model="password" type="password" autocomplete="current-password" class="btn input" />
          </label>
          <button class="btn primary" :disabled="exporting" @click="runExport">
            {{ exporting ? '正在打包…' : '导出并下载' }}
          </button>
          <div v-if="exportErr" class="error">{{ exportErr }}</div>
          <div v-if="exportMsg" class="ok">{{ exportMsg }}</div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.unit { font-size: 13px; color: var(--fg-faint); font-weight: 400; }
.bars { display: flex; align-items: flex-end; gap: 4px; height: 170px; padding-top: 8px; }
.bar-col { flex: 1; display: flex; flex-direction: column; align-items: center; height: 100%; min-width: 0; }
.bar-track { flex: 1; width: 100%; display: flex; align-items: flex-end; }
.bar {
  width: 100%; border-radius: 3px 3px 0 0;
  background: linear-gradient(180deg, #7fb2d9, #4a7fa8);
  transition: height .2s ease;
}
.bar.zero { background: rgba(255, 255, 255, .06); }
.bar-label {
  margin-top: 5px; font-size: 9.5px; color: var(--fg-faint);
  font-family: var(--mono); white-space: nowrap; transform: rotate(-45deg);
}

.heat { display: flex; gap: 8px; overflow-x: auto; padding-bottom: 4px; }
.heat-weekdays {
  display: grid; grid-template-rows: repeat(7, 12px); gap: 3px;
  font-size: 9.5px; color: var(--fg-faint); padding-top: 0;
}
.heat-weekdays span { line-height: 12px; }
.heat-grid { display: flex; gap: 3px; }
.heat-col { display: grid; grid-template-rows: repeat(7, 12px); gap: 3px; }
.heat-cell { width: 12px; height: 12px; border-radius: 2px; background: rgba(255, 255, 255, .04); }
.heat-cell.lv1 { background: rgba(127, 178, 217, .28); }
.heat-cell.lv2 { background: rgba(127, 178, 217, .48); }
.heat-cell.lv3 { background: rgba(127, 178, 217, .72); }
.heat-cell.lv4 { background: rgba(127, 178, 217, 1); }
.heat-cell.empty { background: transparent; }

.warn {
  margin: 0 0 12px; padding: 9px 11px; border-radius: 6px;
  background: rgba(226, 109, 117, .08); border: 1px solid rgba(226, 109, 117, .22);
  color: var(--fg-dim); font-size: 12px; line-height: 1.6;
}
.export-grid { display: grid; grid-template-columns: 1.4fr 1fr; gap: 16px; }
.export-books { min-width: 0; }
.pick-all { display: flex; gap: 8px; align-items: center; font-size: 12.5px; margin-bottom: 8px; }
.book-list {
  border: 1px solid var(--line); border-radius: 6px; max-height: 260px; overflow-y: auto;
}
.book-item {
  display: flex; align-items: center; gap: 8px; padding: 7px 10px;
  border-bottom: 1px solid var(--line); font-size: 12.5px; cursor: pointer;
}
.book-item:last-child { border-bottom: 0; }
.book-item:hover { background: rgba(255, 255, 255, .03); }
.book-title { flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.book-meta { color: var(--fg-faint); font-size: 11px; }
.hint-line { margin-top: 7px; font-size: 11px; color: var(--fg-faint); }
.field { display: flex; flex-direction: column; gap: 6px; margin-bottom: 12px; font-size: 12px; }
.field > span { color: var(--fg-dim); }
.input { width: 100%; }
.btn.primary { width: 100%; }
.ok { margin-top: 8px; font-size: 12px; color: #6fbf8f; }
@media (max-width: 900px) {
  .export-grid { grid-template-columns: 1fr; }
}
</style>
