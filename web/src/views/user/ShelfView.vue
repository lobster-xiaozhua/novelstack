<script setup>
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { state, progressOf } from '../../user/store'
import EmptyState from '../../components/EmptyState.vue'

const router = useRouter()
const q = ref('')

const all = computed(() =>
  [...state.books].sort((a, b) => {
    const pa = progressOf(a.id)?.updated_at || ''
    const pb = progressOf(b.id)?.updated_at || ''
    return pb.localeCompare(pa) || a.title.localeCompare(b.title, 'zh')
  }),
)

const books = computed(() => {
  const key = q.value.trim().toLowerCase()
  if (!key) return all.value
  return all.value.filter(
    (b) =>
      String(b.title || '').toLowerCase().includes(key) ||
      String(b.author || '').toLowerCase().includes(key),
  )
})

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
    <div class="search" role="search" style="margin-bottom:14px">
      <svg width="15" height="15" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.6">
        <circle cx="7" cy="7" r="4.6" />
        <path d="M10.4 10.4 L14 14" stroke-linecap="round" />
      </svg>
      <input v-model="q" type="search" placeholder="搜索书名、作者" aria-label="搜索书架" />
      <button
        v-if="q"
        type="button"
        style="border:0;background:transparent;color:var(--secondary);cursor:pointer;padding:0"
        @click="q = ''"
      >✕</button>
    </div>

    <EmptyState
      v-if="!all.length"
      art="shelf"
      title="书架空空如也"
      desc="同步一本书进来，就能接着上次的位置读。"
      cta="去发现页找书"
      @action="router.push('/discover')"
    />

    <EmptyState
      v-else-if="!books.length"
      art="doc"
      :title="`没有匹配「${q.trim()}」的书`"
      desc="换个关键词试试，或者清空搜索看全部。"
      cta="清空搜索"
      @action="q = ''"
    />

    <div v-else class="rows book-rows">
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
          <div
            v-if="percentOf(b) > 0"
            class="progress"
            style="margin-top:8px;height:3px;background:rgba(120,120,128,.16);border-radius:2px;overflow:hidden"
          >
            <i :style="{ display:'block', height:'100%', width: (percentOf(b) * 100) + '%', background:'var(--blue)', transition:'width .3s ease' }"></i>
          </div>
        </div>
        <span style="color:var(--tertiary);font-size:20px">›</span>
      </router-link>
    </div>
  </div>
</template>
