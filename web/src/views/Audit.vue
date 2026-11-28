<script setup>
import { onMounted, ref } from 'vue'
import { getAudit } from '../api'
import { ts } from '../format'

const rows = ref([])
const loading = ref(false)
const error = ref('')
const action = ref('')
const result = ref('')
const more = ref(false)

const actions = [
  '', 'book_upload', 'book_download', 'bulk_import', 'book_delete', 'book_meta_update',
  'chapter_plain', 'tts_speak', 'meta_search', 'legado_gateway',
  'backup_created', 'kek_rotated', 'cache_cleared', 'cache_pruned',
  'restore_drill', 'login_success', 'login_failed', 'audit_view', 'dashboard_view',
]

async function load(reset = true) {
  loading.value = true
  error.value = ''
  try {
    const params = { limit: 100, action: action.value, result: result.value }
    if (!reset && rows.value.length) params.before_id = rows.value[rows.value.length - 1].id
    const next = await getAudit(params)
    rows.value = reset ? next : rows.value.concat(next)
    more.value = next.length === 100
  } catch (e) {
    error.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

onMounted(() => load(true))
</script>

<template>
  <div>
    <header class="top">
      <div>
        <h1>审计</h1>
        <div class="sub">最近记录 · 查询词只保存 HMAC 指纹与长度，正文永不入库</div>
      </div>
      <div class="actions"><button class="btn" :disabled="loading" @click="load(true)">刷新</button></div>
    </header>

    <div class="card" style="margin-bottom:14px">
      <div class="toolbar">
        <div class="field">
          <label>动作</label>
          <input v-model="action" list="actions" placeholder="全部 / 前缀（如 legado_）" />
          <datalist id="actions">
            <option v-for="a in actions.filter(Boolean)" :key="a" :value="a" />
          </datalist>
        </div>
        <div class="field">
          <label>结果</label>
          <select v-model="result">
            <option value="">全部</option>
            <option value="ok">ok</option>
            <option value="denied">denied</option>
            <option value="error">error</option>
            <option value="ratelimited">ratelimited</option>
            <option value="skipped">skipped</option>
          </select>
        </div>
        <button class="btn" :disabled="loading" @click="load(true)">应用过滤</button>
      </div>
    </div>

    <div v-if="error" class="error">{{ error }}</div>

    <div class="card">
      <table class="tbl" v-if="rows.length">
        <thead>
          <tr><th class="num">#</th><th>时间</th><th>用户</th><th>IP</th><th>动作</th><th>书籍</th><th>结果</th><th>摘要</th></tr>
        </thead>
        <tbody>
          <tr v-for="r in rows" :key="r.id">
            <td class="num" data-label="#">{{ r.id }}</td>
            <td class="mono" data-label="时间" style="white-space:nowrap">{{ ts(r.ts) }}</td>
            <td data-label="用户">{{ r.username || '—' }}</td>
            <td class="mono" data-label="IP">{{ r.ip || '—' }}</td>
            <td data-label="动作">{{ r.action }}</td>
            <td class="mono" data-label="书籍">{{ r.book_id ? r.book_id.slice(0, 8) : '—' }}</td>
            <td data-label="结果"><span class="pill" :class="r.result === 'ok' ? 'ok' : (r.result === 'skipped' ? 'muted' : (r.result === 'denied' || r.result === 'ratelimited' ? 'warn' : 'bad'))">{{ r.result }}</span></td>
            <td class="ellip detail" data-label="摘要">{{ r.detail || '' }}</td>
          </tr>
        </tbody>
      </table>
      <div v-else class="empty">没有匹配记录</div>
      <div style="margin-top:12px" v-if="more">
        <button class="btn ghost" :disabled="loading" @click="load(false)">加载更早</button>
      </div>
    </div>
  </div>
</template>

<style scoped>
.detail { max-width: 340px; }
@media (max-width: 767px) {
  .detail { max-width: none; }
}
</style>
