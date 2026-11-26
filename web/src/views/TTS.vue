<script setup>
import { computed } from 'vue'
import { useDashboard } from '../useDashboard'
import { bytes, ms, pct, ts } from '../format'

const { data, error, loading, updatedAt, load } = useDashboard()
const d = computed(() => data.value)
</script>

<template>
  <div>
    <header class="top">
      <div>
        <h1>TTS 朗读</h1>
        <div class="sub">缓存命中、合成质量与 Provider 健康 · <span v-if="updatedAt">{{ ts(updatedAt) }}</span></div>
      </div>
      <div class="actions"><button class="btn" :disabled="loading" @click="load">刷新</button></div>
    </header>

    <div v-if="error" class="error">{{ error }}</div>
    <div v-else-if="loading && !d" class="loading">加载…</div>

    <template v-if="d">
      <section class="grid cols-4">
        <div class="card kpi">
          <div class="label">Provider</div>
          <div class="value" style="font-size:18px">
            <span class="pill" :class="d.tts.provider.health === 'ok' ? 'ok' : (d.tts.provider.health === 'disabled' ? 'muted' : 'bad')">{{ d.tts.provider.health }}</span>
          </div>
          <div class="foot">{{ d.tts.provider.name || d.tts.provider.kind || 'off' }}</div>
        </div>
        <div class="card kpi">
          <div class="label">缓存命中率</div>
          <div class="value">{{ pct(d.tts.cache.hit_rate) }}</div>
          <div class="foot">最近 200 次朗读</div>
        </div>
        <div class="card kpi">
          <div class="label">缓存占用</div>
          <div class="value">{{ bytes(d.tts.cache.bytes) }}</div>
          <div class="foot">{{ d.tts.cache.segments }} 片段</div>
        </div>
        <div class="card kpi">
          <div class="label">合成成功率</div>
          <div class="value">{{ pct(d.tts.synth.success_rate) }}</div>
          <div class="foot">最近 {{ d.tts.synth.total }} 次</div>
        </div>
      </section>

      <section class="grid cols-2" style="margin-top:14px">
        <div class="card">
          <h2>合成质量 <span class="hint">最近 100 次</span></h2>
          <div class="rows">
            <div class="row"><span class="k">成功 / 失败</span><span class="v">{{ d.tts.synth.ok }} / {{ d.tts.synth.failed }}</span></div>
            <div class="row"><span class="k">平均耗时</span><span class="v">{{ ms(d.tts.synth.avg_ms) }}</span></div>
            <div class="row"><span class="k">P50</span><span class="v">{{ ms(d.tts.synth.p50_ms) }}</span></div>
            <div class="row"><span class="k">P95</span><span class="v">{{ ms(d.tts.synth.p95_ms) }}</span></div>
          </div>
        </div>

        <div class="card">
          <h2>朗读请求 <span class="hint">最近 200 次</span></h2>
          <div class="rows">
            <div class="row"><span class="k">片段总数</span><span class="v">{{ d.tts.speak.segments }}</span></div>
            <div class="row"><span class="k">命中 / 合成</span><span class="v">{{ d.tts.speak.hits }} / {{ d.tts.speak.synths }}</span></div>
            <div class="row"><span class="k">命中率</span><span class="v">{{ pct(d.tts.speak.hit_rate) }}</span></div>
            <div class="row"><span class="k">P50 / P95</span><span class="v">{{ ms(d.tts.speak.p50_ms) }} / {{ ms(d.tts.speak.p95_ms) }}</span></div>
          </div>
        </div>
      </section>

      <section class="card" style="margin-top:14px">
        <h2>按书缓存占用 <span class="hint">按密文字节排序</span></h2>
        <table class="tbl" v-if="d.tts.cache.by_book.length">
          <thead><tr><th>书籍</th><th class="num">片段</th><th class="num">占用</th></tr></thead>
          <tbody>
            <tr v-for="b in d.tts.cache.by_book" :key="b.book_id">
              <td class="ellip">{{ b.title || b.book_id }}</td>
              <td class="num">{{ b.segments }}</td>
              <td class="num">{{ bytes(b.bytes) }}</td>
            </tr>
          </tbody>
        </table>
        <div v-else class="empty">暂无缓存片段</div>
      </section>
    </template>
  </div>
</template>
