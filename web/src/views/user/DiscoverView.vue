<script setup>
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { state, progressOf } from '../../user/store'
import EmptyState from '../../components/EmptyState.vue'

const router = useRouter()

const favorites = computed(() =>
  state.books.filter((b) => b.is_favorite === true || b.is_favorite === 1),
)

const recent = computed(() =>
  [...state.books]
    .sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || '')))
    .slice(0, 12),
)

function status(book) {
  const p = progressOf(book.id)
  if (!p) return '未开始'
  return `读到第 ${p.chapter_index + 1} 章 · ${Math.round((Number(p.percent) || 0) * 100)}%`
}
</script>

<template>
  <div>
    <div v-if="favorites.length" style="margin-bottom:18px">
      <div style="font-size:12px;font-weight:600;letter-spacing:.04em;text-transform:uppercase;color:var(--secondary);margin:0 0 8px 4px">
        收藏
      </div>
      <div class="grid cols-2">
        <router-link
          v-for="b in favorites"
          :key="b.id"
          :to="`/read/${b.id}`"
          class="card hoverable"
          style="color:inherit"
        >
          <div style="display:flex;gap:10px;align-items:center">
            <div class="cover" style="width:36px;height:48px;background:var(--blue-dim);color:var(--blue);display:grid;place-items:center;font-weight:600">
              {{ b.title.slice(0, 1) }}
            </div>
            <div style="min-width:0">
              <div class="ellip" style="font-weight:600">{{ b.title }}</div>
              <div style="font-size:12.5px;color:var(--secondary);margin-top:2px">{{ status(b) }}</div>
            </div>
          </div>
        </router-link>
      </div>
    </div>

    <div style="font-size:12px;font-weight:600;letter-spacing:.04em;text-transform:uppercase;color:var(--secondary);margin:0 0 8px 4px">
      最近入库
    </div>
    <EmptyState
      v-if="!recent.length"
      art="doc"
      title="书库还是空的"
      desc="在控制台上传第一本书，回到这里刷新一下就能看到。"
      cta="去控制台导入"
      @action="router.push('/admin/overview')"
    />
    <div class="grid cols-2" v-else>
      <router-link
        v-for="b in recent"
        :key="b.id"
        :to="`/read/${b.id}`"
        class="card hoverable"
        style="color:inherit"
      >
        <div style="font-weight:600;font-size:15px" class="ellip">{{ b.title }}</div>
        <div style="font-size:12.5px;color:var(--secondary);margin-top:4px">
          {{ b.author || '未知作者' }} · {{ b.chapter_count }} 章
        </div>
        <div style="font-size:12.5px;color:var(--secondary);margin-top:6px">{{ status(b) }}</div>
      </router-link>
    </div>
  </div>
</template>
