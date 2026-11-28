<script setup>
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { state, bookById } from '../../user/store'
import EmptyState from '../../components/EmptyState.vue'

const router = useRouter()

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
    <EmptyState
      v-if="!items.length"
      art="clock"
      title="还没有阅读记录"
      desc="打开一本书读几页，位置就会记在这里；换设备登录也能看到同一份记录。"
      cta="去书架挑一本"
      @action="router.push('/library')"
    />

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
