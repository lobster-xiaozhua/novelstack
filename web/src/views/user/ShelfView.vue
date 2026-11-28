<script setup>
import { computed } from 'vue'
import { state, progressOf } from '../../user/store'

const books = computed(() =>
  [...state.books].sort((a, b) => {
    const pa = progressOf(a.id)?.updated_at || ''
    const pb = progressOf(b.id)?.updated_at || ''
    return pb.localeCompare(pa) || a.title.localeCompare(b.title, 'zh')
  }),
)

function percentOf(book) {
  const p = progressOf(book.id)
  if (!p) return 0
  return Math.max(0, Math.min(1, Number(p.percent) || 0))
}

function caption(book) {
  const p = progressOf(book.id)
  if (!p) return `${book.author || '未知作者'} · ${book.chapter_count} 章 · 未读`
  const chapter = (book.chapters || []).find((c) => c.index === p.chapter_index)
  const name = chapter ? chapter.title : `第 ${p.chapter_index + 1} 章`
  const pct = Math.round(percentOf(book) * 100)
  return `${book.author || '未知作者'} · ${name} · ${pct}%`
}
</script>

<template>
  <div>
    <div v-if="!books.length" class="empty">
      书架还是空的<br />
      <span style="font-size:12.5px">在控制台上传书籍后同步即可</span>
    </div>

    <div v-else class="rows" style="gap:10px;display:flex;flex-direction:column">
      <router-link
        v-for="b in books"
        :key="b.id"
        :to="`/read/${b.id}`"
        class="bookcard card hoverable"
        style="color:inherit;text-decoration:none"
      >
        <div class="cover">{{ b.title.slice(0, 1) }}</div>
        <div class="meta">
          <b>{{ b.title }}</b>
          <span>{{ caption(b) }}</span>
          <div class="progress" style="margin-top:8px;height:3px;background:rgba(120,120,128,.16);border-radius:2px;overflow:hidden">
            <i :style="{ display:'block', height:'100%', width: (percentOf(b) * 100) + '%', background:'var(--blue)', transition:'width .3s ease' }"></i>
          </div>
        </div>
        <span style="color:var(--tertiary);font-size:20px">›</span>
      </router-link>
    </div>
  </div>
</template>
