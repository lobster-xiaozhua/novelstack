<script setup>
import { computed } from 'vue'
import { useDashboard } from '../useDashboard'
import { ms, pct, ts } from '../format'

const { data, error, loading, updatedAt, load } = useDashboard(20000)
const d = computed(() => data.value)

const rotationState = computed(() => {
  if (!d.value) return '—'
  const dist = d.value.kek.distribution || []
  if (!dist.length) return 'empty'
  if (dist.every((x) => x.version === d.value.kek.current_version)) return 'complete'
  return 'pending'
})
</script>

<template>
  <div>
    <header class="top">
      <div>
        <h1>加密健康度</h1>
        <div class="sub">KEK 版本分布、轮换状态与解密失败 · <span v-if="updatedAt">{{ ts(updatedAt) }}</span></div>
      </div>
      <div class="actions"><button class="btn" :disabled="loading" @click="load">刷新</button></div>
    </header>

    <div v-if="error" class="error">{{ error }}</div>
    <div v-else-if="loading && !d" class="loading">加载…</div>

    <template v-if="d">
      <section class="grid cols-3">
        <div class="card kpi">
          <div class="label">当前 KEK 版本</div>
          <div class="value">v{{ d.kek.current_version }}</div>
          <div class="foot">已加载：{{ d.kek.loaded_versions.map(v => 'v' + v).join(', ') || '—' }}</div>
        </div>
        <div class="card kpi">
          <div class="label">轮换状态</div>
          <div class="value" style="font-size:20px">
            <span class="pill" :class="rotationState === 'complete' ? 'ok' : (rotationState === 'pending' ? 'warn' : 'muted')">
              {{ rotationState === 'complete' ? '全部当前版本' : (rotationState === 'pending' ? '存在旧版本' : '无书籍') }}
            </span>
          </div>
          <div class="foot">写入使用 v{{ d.kek.current_version }}</div>
        </div>
        <div class="card kpi">
          <div class="label">最近 24h 解密失败</div>
          <div class="value" :style="{ color: d.kek.decrypt_failures_24h ? 'var(--bad)' : 'var(--ok)' }">{{ d.kek.decrypt_failures_24h }}</div>
          <div class="foot">累计 {{ d.kek.decrypt_failures_total }} 次（来自审计）</div>
        </div>
      </section>

      <section class="grid cols-2" style="margin-top:14px">
        <div class="card">
          <h2>KEK 版本分布</h2>
          <table class="tbl" v-if="d.kek.distribution.length">
            <thead><tr><th>版本</th><th class="num">书籍数</th><th style="width:45%">占比</th></tr></thead>
            <tbody>
              <tr v-for="v in d.kek.distribution" :key="v.version">
                <td data-label="版本"><span class="tag">v{{ v.version }}</span></td>
                <td class="num" data-label="书籍数">{{ v.books }}</td>
                <td data-label="占比">
                  <div class="barline">
                    <div class="bar"><i :class="v.version === d.kek.current_version ? 'ok' : ''" :style="{ width: (d.books.total ? v.books / d.books.total * 100 : 0) + '%' }" /></div>
                    <span class="n">{{ pct(d.books.total ? v.books / d.books.total : 0, 1) }}</span>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
          <div v-else class="empty">书库为空</div>
        </div>

        <div class="card">
          <h2>最近轮换</h2>
          <div v-if="d.kek.last_rotation" class="rows">
            <div class="row"><span class="k">时间</span><span class="v">{{ ts(d.kek.last_rotation.ts) }}</span></div>
            <div class="row"><span class="k">版本</span><span class="v">v{{ d.kek.last_rotation.from_version }} → v{{ d.kek.last_rotation.to_version }}</span></div>
            <div class="row"><span class="k">书籍重包装</span><span class="v">{{ d.kek.last_rotation.books_rotated }} 本</span></div>
            <div class="row"><span class="k">音频重包装</span><span class="v">{{ d.kek.last_rotation.segments_rotated }} 片段</span></div>
            <div class="row"><span class="k">结果</span><span class="v"><span class="pill" :class="d.kek.last_rotation.result === 'ok' ? 'ok' : 'bad'">{{ d.kek.last_rotation.result }}</span></span></div>
          </div>
          <div v-else class="empty">从未轮换</div>
          <div class="note" style="margin-top:14px">
            <b>窗口期约束：</b>轮换期间服务可继续读取（旧版本记录仍用旧 KEK），但建议暂停上传与朗读写入。
            轮换完成后运行 <span class="tag">make kek-verify</span> 抽样校验，确认无误后再人工归档旧 KEK。
          </div>
        </div>
      </section>

      <section class="card" style="margin-top:14px">
        <h2>离线 KEK 检查清单</h2>
        <div class="rows">
          <div class="row"><span class="k">备份包是否包含 KEK？</span><span class="v" style="color:var(--ok)">否（设计上永不包含）</span></div>
          <div class="row"><span class="k">KEK 是否已打印/存入硬件密钥？</span><span class="v">人工确认</span></div>
          <div class="row"><span class="k">旧 KEK 是否已归档？</span><span class="v">仅在全部轮换且校验通过后</span></div>
          <div class="row"><span class="k">恢复演练是否通过？</span><span class="v">{{ d.backup.last_drill ? (d.backup.last_drill.result === 'ok' ? '最近通过' : '最近失败') : '尚未演练' }}</span></div>
        </div>
        <div class="note warn">
          丢失 KEK 等于永久丢失全部书籍与音频缓存；备份包无法替代 KEK 备份。
        </div>
      </section>
    </template>
  </div>
</template>
