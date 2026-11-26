<script setup>
import { computed } from 'vue'
import { useDashboard } from '../useDashboard'
import { ms, pct, ts } from '../format'

const { data, error, loading, updatedAt, load } = useDashboard()
const d = computed(() => data.value)
</script>

<template>
  <div>
    <header class="top">
      <div>
        <h1>搜索与覆盖度</h1>
        <div class="sub">服务端仅索引元数据；正文检索在客户端本地完成 · <span v-if="updatedAt">{{ ts(updatedAt) }}</span></div>
      </div>
      <div class="actions"><button class="btn" :disabled="loading" @click="load">刷新</button></div>
    </header>

    <div v-if="error" class="error">{{ error }}</div>
    <div v-else-if="loading && !d" class="loading">加载…</div>

    <template v-if="d">
      <section class="grid cols-4">
        <div class="card kpi">
          <div class="label">索引后端</div>
          <div class="value" style="font-size:18px">{{ d.search.backend || '—' }}</div>
          <div class="foot">{{ d.search.index || '本地降级匹配' }}</div>
        </div>
        <div class="card kpi">
          <div class="label">索引健康</div>
          <div class="value" style="font-size:18px">
            <span class="pill" :class="d.search.health === 'ok' ? 'ok' : 'warn'">{{ d.search.health }}</span>
          </div>
          <div class="foot">{{ d.search.documents }} 文档</div>
        </div>
        <div class="card kpi">
          <div class="label">文档偏差</div>
          <div class="value" :style="{ color: d.search.drift === 0 ? 'var(--ok)' : 'var(--warn)' }">
            {{ d.search.drift > 0 ? '+' : '' }}{{ d.search.drift }}
          </div>
          <div class="foot">索引 - 书籍总数</div>
        </div>
        <div class="card kpi">
          <div class="label">查询延迟</div>
          <div class="value" style="font-size:20px">{{ ms(d.search.latency.p50_ms) }}</div>
          <div class="foot">P95 {{ ms(d.search.latency.p95_ms) }} · {{ d.search.latency.samples }} 样本</div>
        </div>
      </section>

      <section class="card" style="margin-top:14px">
        <h2>客户端索引覆盖度 <span class="hint">已回执章节 / 总章节</span></h2>
        <table class="tbl" v-if="d.search.coverage.length">
          <thead><tr><th>书籍</th><th class="num">已回执</th><th class="num">总章节</th><th style="width:40%">覆盖</th></tr></thead>
          <tbody>
            <tr v-for="c in d.search.coverage" :key="c.book_id">
              <td class="ellip">{{ c.title }}</td>
              <td class="num">{{ c.indexed_chapters }}</td>
              <td class="num">{{ c.total_chapters }}</td>
              <td>
                <div class="barline">
                  <div class="bar"><i :class="c.coverage >= 1 ? 'ok' : ''" :style="{ width: Math.min(100, c.coverage * 100) + '%' }" /></div>
                  <span class="n">{{ pct(c.coverage, 0) }}</span>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty">暂无客户端回执</div>
        <div class="note">
          覆盖度来自客户端同步后的 <span class="tag">POST /ack</span> 回执；未回执不代表服务端不可用，只表示该设备尚未建立本地 FTS 索引。
        </div>
      </section>
    </template>
  </div>
</template>
