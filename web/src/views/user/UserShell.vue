<script setup>
import { computed, onMounted, onUnmounted } from 'vue'
import { useRoute } from 'vue-router'
import { boot, state, refreshProgress, flushProgress } from '../../user/store'

const route = useRoute()

const titles = {
  'user-shelf': ['书架', '按更新时间排序 · 点开继续阅读'],
  'user-discover': ['发现', '最近更新的书与收藏'],
  'user-history': ['足迹', '所有书的阅读位置（多端合并后）'],
  'user-settings': ['我的', '账号、缓存与同步'],
}
const heading = computed(() => (titles[route.name] || ['novelstack', ''])[0])
const sub = computed(() => (titles[route.name] || ['', ''])[1])

let timer = null
let onVisible = null

onMounted(async () => {
  await boot()
  // 前台每 5 分钟拉一次增量进度（与客户端策略一致，不用长连接）。
  timer = setInterval(() => refreshProgress(), 5 * 60 * 1000)
  onVisible = () => {
    if (document.visibilityState === 'visible') {
      refreshProgress()
      flushProgress()
    }
  }
  document.addEventListener('visibilitychange', onVisible)
})

onUnmounted(() => {
  if (timer) clearInterval(timer)
  if (onVisible) document.removeEventListener('visibilitychange', onVisible)
})
</script>

<template>
  <div class="shell">
    <header class="top" style="padding:18px 16px 0;max-width:980px;margin:0 auto;width:100%">
      <div>
        <h1>{{ heading }}</h1>
        <div class="sub">
          {{ sub }}
          <span v-if="state.offline" class="pill warn" style="margin-left:8px">离线</span>
        </div>
      </div>
    </header>
    <main class="content">
      <div v-if="state.loading" class="card">
        <div class="skeleton sk-line" style="width:38%"></div>
        <div class="skeleton sk-card" style="margin-top:14px"></div>
        <div class="skeleton sk-card"></div>
        <div class="skeleton sk-card"></div>
      </div>
      <div v-else-if="state.error" class="error">{{ state.error }}</div>
      <router-view v-else />
    </main>
    <nav class="tabbar">
      <router-link to="/library" :class="{ active: $route.path.startsWith('/library') }">
        <span class="ic">▤</span><span>书架</span>
      </router-link>
      <router-link to="/discover" :class="{ active: $route.path.startsWith('/discover') }">
        <span class="ic">◈</span><span>发现</span>
      </router-link>
      <router-link to="/history" :class="{ active: $route.path.startsWith('/history') }">
        <span class="ic">◷</span><span>足迹</span>
      </router-link>
      <router-link to="/settings" :class="{ active: $route.path.startsWith('/settings') }">
        <span class="ic">⚙</span><span>我的</span>
      </router-link>
    </nav>
  </div>
</template>
