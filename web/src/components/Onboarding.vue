<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { markOnboarded } from '../user/guide'

const emit = defineEmits(['done'])

// 三页为限：再多用户就开始划着跳过，起不到引导作用。
const slides = [
  {
    no: '01',
    title: '数据绝对私密',
    desc: '正文以 AES-GCM 信封加密存放，密钥只在你的设备上解开。服务端拿到的只有密文。',
    art: 'shield',
  },
  {
    no: '02',
    title: '沉浸阅读',
    desc: '左右滑动翻页，长按取词，一键听书。排版按中文长文的节奏调过行高与字距。',
    art: 'book',
  },
  {
    no: '03',
    title: '多端同步',
    desc: '手机读到第几章，网页打开就接着读。按「最远位置」合并，谁也不会把谁拽回去。',
    art: 'sync',
  },
]

const track = ref(null)
const index = ref(0)

function onScroll() {
  const el = track.value
  if (!el) return
  const w = el.clientWidth || 1
  index.value = Math.min(slides.length - 1, Math.max(0, Math.round(el.scrollLeft / w)))
}

function go(i) {
  const el = track.value
  if (!el) return
  el.scrollTo({ left: el.clientWidth * i, behavior: 'smooth' })
}

function next() {
  if (index.value >= slides.length - 1) finish()
  else go(index.value + 1)
}

function finish() {
  markOnboarded()
  emit('done')
}

const last = computed(() => index.value >= slides.length - 1)

onMounted(() => {
  // 桌面端没有触摸滑动，补上左右方向键与滚轮横向滚动。
  window.addEventListener('keydown', onKey)
})

function onKey(e) {
  if (e.key === 'ArrowRight') next()
  else if (e.key === 'ArrowLeft' && index.value > 0) go(index.value - 1)
  else if (e.key === 'Escape') finish()
}

// 组件卸载时移除监听（引导是一次性的，通常立即卸载）。
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <div class="onboard" role="dialog" aria-label="欢迎页">
    <button class="onboard-skip" type="button" @click="finish">跳过</button>

    <div ref="track" class="onboard-track" @scroll.passive="onScroll">
      <section v-for="s in slides" :key="s.no" class="onboard-page">
        <div class="onboard-art">
          <!-- 极简细线条插画：只有描边，没有填充和阴影 -->
          <svg v-if="s.art === 'shield'" viewBox="0 0 200 150" fill="none" stroke="currentColor"
               stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
            <path d="M100 16 L148 33 V74 C148 101 127 121 100 132 C73 121 52 101 52 74 V33 Z" />
            <rect x="76" y="58" width="48" height="33" rx="5" />
            <path d="M77 63 L100 80 L123 63" />
            <path d="M100 132 V140" stroke-dasharray="3 5" />
          </svg>

          <svg v-else-if="s.art === 'book'" viewBox="0 0 200 150" fill="none" stroke="currentColor"
               stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
            <path d="M26 44 C48 36 76 38 96 47 V118 C76 109 48 107 26 115 Z" />
            <path d="M174 44 C152 36 124 38 104 47 V118 C124 109 152 107 174 115 Z" />
            <path d="M96 47 V118" />
            <path d="M40 60 C52 57 64 57 76 60" />
            <path d="M40 72 C52 69 64 69 76 72" />
            <path d="M124 60 C136 57 148 57 160 60" />
            <path d="M124 72 C136 69 148 69 160 72" />
            <path d="M168 24 C176 32 176 42 168 50" opacity=".55" />
            <path d="M176 16 C188 28 188 46 176 58" opacity=".3" />
          </svg>

          <svg v-else viewBox="0 0 200 150" fill="none" stroke="currentColor"
               stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
            <rect x="24" y="42" width="52" height="80" rx="9" />
            <path d="M44 112 H56" />
            <rect x="112" y="54" width="62" height="44" rx="6" />
            <path d="M100 112 H186" />
            <path d="M143 98 V112" />
            <path d="M86 74 H104" />
            <path d="M96 66 L104 74 L96 82" />
            <path d="M120 34 C128 28 138 28 146 34" opacity=".45" />
            <path d="M114 26 C126 18 140 18 152 26" opacity=".25" />
          </svg>
        </div>

        <div class="onboard-no">{{ s.no }}</div>
        <h2>{{ s.title }}</h2>
        <p>{{ s.desc }}</p>
      </section>
    </div>

    <div class="onboard-foot">
      <div class="onboard-dots">
        <i v-for="(s, i) in slides" :key="s.no" :class="{ on: i === index }"></i>
      </div>
      <button class="btn onboard-btn" type="button" @click="next">
        {{ last ? '开始使用' : '下一步' }}
      </button>
    </div>
  </div>
</template>
