<script setup>
import { computed } from 'vue'
import { useDashboard } from '../useDashboard'
import { ago, bytes, ms, pct, shortHash, ts } from '../format'
import HealthBanner from '../components/HealthBanner.vue'

const { data, error, loading, updatedAt, load } = useDashboard()

const d = computed(() => data.value)
const warnings = computed(() => {
  if (!d.value) return []
  const out = []
  const b = d.value.backup || {}
  // 备份相关告警由顶部健康横幅统一呈现，这里只保留演练提醒。
  if (b.last_drill && b.last_drill.result !== 'ok') out.push('最近一次恢复演练失败：备份可能不可用，请立即排查。')
  if (!b.last_drill) out.push('尚未做过恢复演练：建议每季度手动执行一次 restore-drill。')
  if (d.value.books.cipher_drift !== 0) out.push(`密文文件数与书籍记录偏差 ${d.value.books.cipher_drift}，需人工核对磁盘。`)
  if (d.value.kek.decrypt_failures_24h > 0) out.push(`最近 24 小时有 ${d.value.kek.decrypt_failures_24h} 次解密失败（见审计）。`)
  const dist = d.value.kek.distribution || []
  if (dist.some((x) => x.version !== d.value.kek.current_version)) out.push('存在旧 KEK 版本的书籍：轮换尚未完成。')
  return out
})

const healthPills = computed(() => {
  if (!d.value) return []
  return [
    { label: '索引', value: d.value.search.health, cls: d.value.search.health === 'ok' ? 'ok' : 'warn' },
    { label: 'TTS', value: d.value.tts.provider.health, cls: d.value.tts.provider.health === 'ok' ? 'ok' : (d.value.tts.provider.health === 'disabled' ? 'muted' : 'bad') },
    { label: '书源网关', value: d.value.system.legado_enabled ? 'enabled' : 'off', cls: d.value.system.legado_enabled ? 'ok' : 'muted' },
  ]
})

const topCoverage = computed(() => (d.value?.search?.coverage || []).slice(0, 6))
</script>

<template>
  <div>
    <header class="top">
      <div>
        <h1>运营概览</h1>
        <div class="sub">
          只读指标 · 不含章节正文
          <template v-if="updatedAt"> · 更新于 {{ ts(updatedAt) }}</template>
        </div>
      </div>
      <div class="actions">
        <span v-for="p in healthPills" :key="p.label" class="pill" :class="p.cls">{{ p.label }} {{ p.value }}</span>
        <button class="btn" :disabled="loading" @click="load">刷新</button>
      </div>
    </header>

    <HealthBanner :health="d?.health || null" />

    <div v-if="error" class="error">{{ error }}</div>
    <div v-else-if="loading && !d" class="loading">加载指标…</div>

    <template v-if="d">
      <div class="note warn" v-if="warnings.length" style="margin-top:0;margin-bottom:14px">
        <div v-for="w in warnings" :key="w">· {{ w }}</div>
      </div>

      <section class="grid cols-4">
        <div class="card kpi">
          <div class="label">书籍总数</div>
          <div class="value">{{ d.books.total }}<small>本</small></div>
          <div class="foot">{{ d.books.chapters }} 章 · 已删除 {{ d.books.deleted }}</div>
        </div>
        <div class="card kpi">
          <div class="label">密文总量</div>
          <div class="value">{{ bytes(d.books.cipher_bytes) }}</div>
          <div class="foot">{{ d.books.cipher_files }} 个密文文件</div>
        </div>
        <div class="card kpi">
          <div class="label">TTS 缓存</div>
          <div class="value">{{ bytes(d.tts.cache.bytes) }}</div>
          <div class="foot">{{ d.tts.cache.segments }} 片段 · 命中率 {{ pct(d.tts.cache.hit_rate) }}</div>
        </div>
        <div class="card kpi">
          <div class="label">元数据索引偏差</div>
          <div class="value" :style="{ color: d.search.drift === 0 ? 'var(--ok)' : 'var(--warn)' }">
            {{ d.search.drift > 0 ? '+' : '' }}{{ d.search.drift }}
          </div>
          <div class="foot">索引 {{ d.search.documents }} / 书籍 {{ d.books.total }}</div>
        </div>
      </section>

      <section class="grid cols-2" style="margin-top:14px">
        <div class="card">
          <h2>最近备份 <span class="hint">离线加密包</span></h2>
          <div v-if="d.backup.last" class="rows">
            <div class="row"><span class="k">时间</span><span class="v">{{ ts(d.backup.last.ts) }} · {{ ago(d.backup.last.ts) }}</span></div>
            <div class="row"><span class="k">SHA-256</span><span class="v">{{ shortHash(d.backup.last.sha256, 20) }}</span></div>
            <div class="row"><span class="k">大小</span><span class="v">{{ bytes(d.backup.last.size_bytes) }}</span></div>
            <div class="row"><span class="k">目录占用</span><span class="v">{{ bytes(d.backups_dir_bytes) }}（backups/ 合计）</span></div>
            <div class="row"><span class="k">覆盖</span><span class="v">{{ d.backup.last.books }} 本 / {{ d.backup.last.segments }} 片段</span></div>
            <div class="row"><span class="k">文件</span><span class="v">{{ d.backup.last.path }}</span></div>
          </div>
          <div v-else class="empty">尚未生成备份</div>
          <h2 style="margin-top:18px">最近恢复演练</h2>
          <div v-if="d.backup.last_drill" class="rows">
            <div class="row">
              <span class="k">结果</span>
              <span class="v"><span class="pill" :class="d.backup.last_drill.result === 'ok' ? 'ok' : 'bad'">{{ d.backup.last_drill.result }}</span></span>
            </div>
            <div class="row"><span class="k">时间</span><span class="v">{{ ts(d.backup.last_drill.ts) }}</span></div>
            <div class="row"><span class="k">验证书籍</span><span class="v">{{ shortHash(d.backup.last_drill.book_id, 12) }}</span></div>
            <div class="row"><span class="k">耗时</span><span class="v">{{ ms(d.backup.last_drill.duration_ms) }}</span></div>
            <div class="row" v-if="d.backup.last_drill.detail"><span class="k">原因</span><span class="v">{{ d.backup.last_drill.detail }}</span></div>
          </div>
          <div v-else class="empty">尚未演练</div>
        </div>

        <div class="card">
          <h2>加密健康度 <span class="hint">KEK v{{ d.kek.current_version }}</span></h2>
          <div class="rows">
            <div class="row"><span class="k">已加载 KEK 版本</span><span class="v">{{ d.kek.loaded_versions.map(v => 'v' + v).join(', ') || '—' }}</span></div>
            <div class="row"><span class="k">最近 24h 解密失败</span><span class="v" :style="{ color: d.kek.decrypt_failures_24h ? 'var(--bad)' : 'var(--ok)' }">{{ d.kek.decrypt_failures_24h }}</span></div>
            <div class="row"><span class="k">累计解密失败</span><span class="v">{{ d.kek.decrypt_failures_total }}</span></div>
            <div class="row" v-if="d.kek.last_rotation"><span class="k">最近轮换</span><span class="v">v{{ d.kek.last_rotation.from_version }} → v{{ d.kek.last_rotation.to_version }} · {{ ts(d.kek.last_rotation.ts) }}</span></div>
          </div>
          <div v-for="v in d.kek.distribution" :key="v.version" style="margin-top:12px">
            <div class="barline">
              <span class="tag">v{{ v.version }}</span>
              <div class="bar"><i :style="{ width: (d.books.total ? (v.books / d.books.total * 100) : 0) + '%' }" /></div>
              <span class="n">{{ v.books }} 本</span>
            </div>
          </div>
          <div v-if="!d.kek.distribution.length" class="empty">暂无书籍</div>
        </div>
      </section>

      <section class="grid cols-2" style="margin-top:14px">
        <div class="card">
          <h2>客户端索引覆盖度 <span class="hint">已 ack / 总章节</span></h2>
          <table class="tbl" v-if="topCoverage.length">
            <thead><tr><th>书籍</th><th class="num">章节</th><th style="width:38%">覆盖</th></tr></thead>
            <tbody>
              <tr v-for="c in topCoverage" :key="c.book_id">
                <td class="ellip" data-label="书籍">{{ c.title }}</td>
                <td class="num" data-label="章节">{{ c.indexed_chapters }}/{{ c.total_chapters }}</td>
                <td data-label="覆盖"><div class="barline"><div class="bar"><i :style="{ width: (c.coverage * 100) + '%' }" /></div><span class="n">{{ pct(c.coverage, 0) }}</span></div></td>
              </tr>
            </tbody>
          </table>
          <div v-else class="empty">暂无回执</div>
        </div>

        <div class="card">
          <h2>最近审计 <span class="hint">仅摘要，无正文/查询词</span></h2>
          <table class="tbl" v-if="d.audit.recent.length">
            <thead><tr><th>时间</th><th>动作</th><th>结果</th></tr></thead>
            <tbody>
              <tr v-for="a in d.audit.recent.slice(0, 8)" :key="a.id">
                <td class="mono" data-label="时间">{{ ts(a.ts) }}</td>
                <td data-label="动作">{{ a.action }}</td>
                <td data-label="结果"><span class="pill" :class="a.result === 'ok' ? 'ok' : (a.result === 'denied' || a.result === 'ratelimited' ? 'warn' : 'bad')">{{ a.result }}</span></td>
              </tr>
            </tbody>
          </table>
          <div v-else class="empty">暂无记录</div>
        </div>
      </section>
    </template>
  </div>
</template>
