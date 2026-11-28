<script setup>
import { computed, ref } from 'vue'
import { clearCache, pruneCache } from '../api'
import { bytes, ts } from '../format'
import { useDashboard } from '../useDashboard'

const { data, error: derr, loading, load } = useDashboard()
const d = computed(() => data.value)

const modal = ref(null) // {scope:'all'|'book', bookId, title}
const confirmChecked = ref(false)
const busy = ref(false)
const opError = ref('')
const lastClear = ref(null)

const prune = ref({ maxMB: 512, keepDays: 0, dryRun: true })
const lastPrune = ref(null)

function openClear(scope, bookId = '', title = '') {
  if (d.value?.cache.protect_window_s) { /* 提示保护窗口 */ }
  confirmChecked.value = false
  opError.value = ''
  modal.value = { scope, bookId, title }
}

async function doClear() {
  if (!confirmChecked.value) return
  busy.value = true
  opError.value = ''
  try {
    lastClear.value = await clearCache({
      confirm: true,
      all: modal.value.scope === 'all',
      book_id: modal.value.scope === 'book' ? modal.value.bookId : '',
    })
    modal.value = null
    await load()
  } catch (e) {
    opError.value = e.message || String(e)
  } finally {
    busy.value = false
  }
}

async function doPrune() {
  busy.value = true
  opError.value = ''
  try {
    lastPrune.value = await pruneCache({
      confirm: true,
      dry_run: prune.value.dryRun,
      max_bytes: Math.max(0, Number(prune.value.maxMB || 0)) * 1024 * 1024,
      keep_days: Math.max(0, Number(prune.value.keepDays || 0)),
    })
    if (!prune.value.dryRun) await load()
  } catch (e) {
    opError.value = e.message || String(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div>
    <header class="top">
      <div>
        <h1>缓存管理</h1>
        <div class="sub">
          TTS 音频缓存（密文）· 保护窗口 {{ d ? d.cache.protect_window_s : '—' }} 秒 · 正在朗读的片段不会被删除
        </div>
      </div>
      <div class="actions">
        <button class="btn" :disabled="loading" @click="load">刷新</button>
        <button class="btn danger" @click="openClear('all')">清空全部缓存</button>
      </div>
    </header>

    <div v-if="derr" class="error">{{ derr }}</div>
    <div v-if="opError" class="error" style="margin-bottom:12px">{{ opError }}</div>

    <template v-if="d">
      <section class="grid cols-3">
        <div class="card kpi">
          <div class="label">缓存总大小</div>
          <div class="value">{{ bytes(d.cache.bytes) }}</div>
        </div>
        <div class="card kpi">
          <div class="label">片段数</div>
          <div class="value">{{ d.cache.segments }}</div>
        </div>
        <div class="card kpi">
          <div class="label">保护窗口</div>
          <div class="value">{{ d.cache.protect_window_s }}<small>秒</small></div>
          <div class="foot">窗口内访问过的片段不参与淘汰</div>
        </div>
      </section>

      <section class="card" style="margin-top:14px">
        <h2>按书占用</h2>
        <table class="tbl" v-if="d.cache.by_book.length">
          <thead><tr><th>书籍</th><th class="num">片段</th><th class="num">占用</th><th style="width:120px"></th></tr></thead>
          <tbody>
            <tr v-for="b in d.cache.by_book" :key="b.book_id">
              <td class="ellip" data-label="书籍">{{ b.title || b.book_id }}</td>
              <td class="num" data-label="片段">{{ b.segments }}</td>
              <td class="num" data-label="占用">{{ bytes(b.bytes) }}</td>
              <td style="text-align:right" data-label="操作">
                <button class="btn ghost" @click="openClear('book', b.book_id, b.title || b.book_id)">清理</button>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty">暂无缓存</div>
      </section>

      <section class="grid cols-2" style="margin-top:14px">
        <div class="card">
          <h2>按大小 / 天数淘汰</h2>
          <div class="toolbar">
            <div class="field">
              <label>总大小上限（MB）</label>
              <input v-model.number="prune.maxMB" type="number" min="0" step="64" />
            </div>
            <div class="field">
              <label>保留最近（天）</label>
              <input v-model.number="prune.keepDays" type="number" min="0" step="1" />
            </div>
            <label class="field" style="flex-direction:row;align-items:center;gap:7px;padding-bottom:7px">
              <input v-model="prune.dryRun" type="checkbox" style="width:auto" />
              <span style="text-transform:none;letter-spacing:0">仅预演</span>
            </label>
            <button class="btn" :disabled="busy" @click="doPrune">{{ prune.dryRun ? '预演淘汰' : '执行淘汰' }}</button>
          </div>
          <div class="rows" v-if="lastPrune" style="margin-top:12px">
            <div class="row"><span class="k">删除片段</span><span class="v">{{ lastPrune.removed }}</span></div>
            <div class="row"><span class="k">释放</span><span class="v">{{ bytes(lastPrune.freed_bytes) }}</span></div>
            <div class="row"><span class="k">保护跳过</span><span class="v">{{ lastPrune.protected }}</span></div>
            <div class="row"><span class="k">之后大小</span><span class="v">{{ bytes(lastPrune.bytes_after) }}</span></div>
          </div>
          <div class="note">
            默认不自动淘汰。淘汰按最久未访问排序，跳过最近 {{ d.cache.protect_window_s }} 秒内访问过以及正在朗读引用的片段。
          </div>
        </div>

        <div class="card">
          <h2>最近一次手工清理</h2>
          <div v-if="lastClear" class="rows">
            <div class="row"><span class="k">范围</span><span class="v">{{ lastClear.scope === 'all' ? '全部' : lastClear.book_id }}</span></div>
            <div class="row"><span class="k">删除片段</span><span class="v">{{ lastClear.removed }}</span></div>
            <div class="row"><span class="k">释放</span><span class="v">{{ bytes(lastClear.freed_bytes) }}</span></div>
            <div class="row"><span class="k">保护跳过</span><span class="v">{{ lastClear.protected }}</span></div>
            <div class="row"><span class="k">之后大小</span><span class="v">{{ bytes(lastClear.bytes_after) }}</span></div>
          </div>
          <div v-else class="empty">本次会话尚未清理</div>
          <div class="note warn">清理会写审计（cache_cleared）。删除的只是可再合成的音频缓存，不影响书籍密文。</div>
        </div>
      </section>
    </template>

    <div v-if="modal" class="overlay" @click.self="modal = null">
      <div class="modal">
        <h3>{{ modal.scope === 'all' ? '清空全部 TTS 缓存' : '清理该书缓存' }}</h3>
        <p v-if="modal.scope === 'all'">
          将删除所有未被引用的音频片段（密文），书籍与索引不受影响。下次朗读会重新合成。
        </p>
        <p v-else>
          目标：<b>{{ modal.title }}</b><br />
          只删除该书的音频缓存片段，不影响其它书籍。
        </p>
        <label style="display:flex;gap:8px;align-items:flex-start;color:var(--fg-dim);font-size:13px">
          <input v-model="confirmChecked" type="checkbox" style="margin-top:3px" />
          我确认执行清理，并理解朗读中的片段会被保护跳过。
        </label>
        <div class="modal-actions">
          <button class="btn ghost" :disabled="busy" @click="modal = null">取消</button>
          <button class="btn danger" :disabled="busy || !confirmChecked" @click="doClear">
            {{ busy ? '清理中…' : '确认清理' }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
