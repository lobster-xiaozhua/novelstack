<script setup>
import { computed } from 'vue'
import { state, bookById } from '../../user/store'

const items = computed(() =>
  Object.values(state.progress)
    .filter((p) => bookById(p.book_id))
    .sort((a, b) => String(b.updated_at || '').localeCompare(String(a.updated_at || ''))),
)

function rel(iso) {
  if (!iso) return ''
  const t = new Date(iso).getTime()
  if (!t) return ''
  const sec = Math.max(0, (Date.now() - t) / 1000)
  if (sec < 60) return '刚刚'
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`
  if (sec < 86400 * 30) return `${Math.floor(sec / 86400)} 天前`
  return iso.slice(0, 10)
}
</script>

<template>
  <div>
    <div v-if="!items.length" class="empty">
      还没有阅读记录<br />
      <span style="font-size:12.5px">打开一本书读几页就会出现在这里</span>
    </div>

    <div v-else class="card groupcard" style="padding:4px">
      <router-link
        v-for="p in items"
        :key="p.book_id"
        :to="`/read/${p.book_id}`"
        class="row"
        style="color:inherit;text-decoration:none"
      >
        <div style="min-width:0">
          <div class="ellip" style="font-weight:500">{{ bookById(p.book_id).title }}</div>
          <div style="font-size:12.5px;color:var(--secondary);margin-top:2px">
            第 {{ p.chapter_index + 1 }} 章 · {{ Math.round((Number(p.percent) || 0) * 100) }}%
          </div>
        </div>
        <div class="dim" style="font-size:12.5px;white-space:nowrap">{{ rel(p.updated_at) }}</div>
      </router-link>
    </div>
  </div>
</template>
