<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { boot, state, refreshProgress, flushProgress } from '../../user/store'
import { coachSeen, hasOnboarded, markCoachSeen } from '../../user/guide'
import Onboarding from '../../components/Onboarding.vue'
import CoachMarks from '../../components/CoachMarks.vue'

const route = useRoute()

const titles = {
  'user-shelf': ['书架', '按更新时间排序 · 点开继续阅读'],
  'user-discover': ['发现', '最近更新的书与收藏'],
  'user-history': ['足迹', '所有书的阅读位置（多端合并后）'],
  'user-settings': ['我的', '账号、缓存与同步'],
  'user-guide': ['使用指南', '四个最常被问到的问题'],
}
const heading = computed(() => (titles[route.name] || ['novelstack', ''])[0])
const sub = computed(() => (titles[route.name] || ['', ''])[1])

let timer = null
let onVisible = null

// ---------- 新手引导 ----------
const showOnboard = ref(false)
const coachOpen = ref(false)
const coachSteps = ref([])
let coachKey = ''
// boot() 内部会把 loading 置回 false，那一刻 watcher 会先跑一轮；
// 此时还没判断要不要弹欢迎页，贸然开引导会在欢迎页背后藏一层蒙层。
const booted = ref(false)

const STEPS = {
  shelf: [
    {
      sel: '.search',
      title: '先学会搜索',
      body: '书架多了以后，敲两个字就能筛出目标；清空搜索回到全部。',
    },
    {
      sel: '.bookcard',
      title: '点封面接着读',
      body: '卡片上标着读到第几章和百分比，点进去直接落在上次的位置。',
    },
    {
      sel: '.tabbar a[href="/discover"]',
      title: '找不到书就去发现页',
      body: '发现页列出最近入库与收藏，点一本直接开读，进度自动记。',
    },
  ],
  discover: [
    {
      sel: '.grid',
      title: '这里是书库',
      body: '按入库时间排，带「读到第几章」的标记；点封面继续上次的位置。',
    },
  ],
}

function openCoach(name) {
  if (!name || showOnboard.value || coachOpen.value || coachSeen(name)) return
  if (!STEPS[name]) return
  const steps = STEPS[name].filter((s) => document.querySelector(s.sel))
  if (!steps.length) {
    markCoachSeen(name)
    return
  }
  coachKey = name
  coachSteps.value = steps
  coachOpen.value = true
}

function coachNameFor(routeName) {
  if (routeName === 'user-shelf') return 'shelf'
  if (routeName === 'user-discover') return 'discover'
  return ''
}

function onCoachFinish() {
  if (coachKey) markCoachSeen(coachKey)
  coachOpen.value = false
}

function onOnboardDone() {
  showOnboard.value = false
  nextTick(() => openCoach(coachNameFor(route.name)))
}

watch(
  [() => route.name, () => state.loading],
  ([name, loading]) => {
    // 骨架屏阶段书架还没渲染，此时找目标元素必然找不到——等首屏数据落地再量。
    if (!booted.value || loading || showOnboard.value || coachOpen.value) return
    nextTick(() => openCoach(coachNameFor(name)))
  },
)

onMounted(async () => {
  await boot()
  booted.value = true
  // 第一次进来先看欢迎页；看完（或跳过）再触发蒙层引导。
  if (!hasOnboarded()) showOnboard.value = true
  else if (!state.loading) nextTick(() => openCoach(coachNameFor(route.name)))

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
      <div class="brandline">novelstack<span>私人阅读站</span></div>
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

    <Onboarding v-if="showOnboard" @done="onOnboardDone" />
    <CoachMarks v-model="coachOpen" :steps="coachSteps" @finish="onCoachFinish" />
  </div>
</template>
