<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'

/**
 * 蒙层高亮引导（Coach Marks / Spotlight）。
 *
 * 做法：一整层半透明黑，用 clip-path 的 path() 挖一个圆角孔。
 * 关键是 nonzero 填充规则——把内圈矩形**逆时针**写，与顺时针的外框抵消，
 * 于是内圈变成透明（露出被高亮的组件），而不是再多画一块黑色。
 * 所以这里不能图省事用 polygon()：polygon 不闭合内圈，挖不出洞。
 */
const props = defineProps({
  modelValue: { type: Boolean, default: false },
  steps: { type: Array, default: () => [] },
})
const emit = defineEmits(['update:modelValue', 'finish'])

const HOLE_PAD = 8
const CORNER = 14
const GAP = 12

const index = ref(0)
const rect = ref(null)
const bubbleH = ref(120)
const bubbleRef = ref(null)
// 退场：先让蒙层与气泡淡出，再真正卸载。直接 v-if=false 会「啪」一下消失。
const closing = ref(false)
const vw = ref(typeof window === 'undefined' ? 1200 : window.innerWidth)
const vh = ref(typeof window === 'undefined' ? 800 : window.innerHeight)

// 只有此刻真的存在于 DOM 里的目标才参与引导，避免「指向空气」。
const live = computed(() =>
  props.steps.filter((s) => {
    if (typeof document === 'undefined') return false
    const el = document.querySelector(s.sel)
    return !!el && el.getBoundingClientRect().width > 0
  }),
)
const total = computed(() => Math.max(1, live.value.length))
const current = computed(() => live.value[Math.min(index.value, live.value.length - 1)] || null)

function roundRect(x, y, w, h, r, reverse) {
  const p = [`M${x + r} ${y}`]
  if (!reverse) {
    p.push(
      `H${x + w - r}`,
      `A${r} ${r} 0 0 1 ${x + w} ${y + r}`,
      `V${y + h - r}`,
      `A${r} ${r} 0 0 1 ${x + w - r} ${y + h}`,
      `H${x + r}`,
      `A${r} ${r} 0 0 1 ${x} ${y + h - r}`,
      `V${y + r}`,
      `A${r} ${r} 0 0 1 ${x + r} ${y}`,
      'Z',
    )
  } else {
    p.push(
      `A${r} ${r} 0 0 0 ${x} ${y + r}`,
      `V${y + h - r}`,
      `A${r} ${r} 0 0 0 ${x + r} ${y + h}`,
      `H${x + w - r}`,
      `A${r} ${r} 0 0 0 ${x + w} ${y + h - r}`,
      `V${y + r}`,
      `A${r} ${r} 0 0 0 ${x + w - r} ${y}`,
      `H${x + r}`,
      'Z',
    )
  }
  return p.join(' ')
}

const hole = computed(() => {
  const r = rect.value
  if (!r) return null
  const x = Math.max(0, r.left - HOLE_PAD)
  const y = Math.max(0, r.top - HOLE_PAD)
  const w = Math.min(vw.value - x, r.width + HOLE_PAD * 2)
  const h = Math.min(vh.value - y, r.height + HOLE_PAD * 2)
  return { x, y, w, h }
})

const clipPath = computed(() => {
  const h = hole.value
  if (!h) return 'none'
  return `path("M0 0 H${vw.value} V${vh.value} H0 Z ${roundRect(h.x, h.y, h.w, h.h, CORNER, true)}")`
})

const ringStyle = computed(() =>
  hole.value
    ? {
        left: `${hole.value.x}px`,
        top: `${hole.value.y}px`,
        width: `${hole.value.w}px`,
        height: `${hole.value.h}px`,
      }
    : { display: 'none' },
)

const below = computed(() => {
  const h = hole.value
  if (!h) return true
  return h.y + h.h + GAP + bubbleH.value + 16 < vh.value
})

const bubbleStyle = computed(() => {
  const h = hole.value
  if (!h) return { display: 'none' }
  const width = Math.min(300, vw.value - 24)
  let left = h.x + h.w / 2 - width / 2
  left = Math.max(12, Math.min(vw.value - width - 12, left))
  // 变量不能叫 top：脚本顶层 top 是 window.top，const 重名会直接抛错。
  const topY = below.value ? h.y + h.h + GAP : h.y - GAP - bubbleH.value
  return {
    left: `${left}px`,
    top: `${Math.max(12, topY)}px`,
    width: `${width}px`,
  }
})

const arrowStyle = computed(() => {
  const h = hole.value
  const b = bubbleStyle.value
  if (!h || !b.left) return { display: 'none' }
  const left = Math.max(Number.parseFloat(b.left) + 18, Math.min(
    Number.parseFloat(b.left) + Number.parseFloat(b.width) - 30,
    h.x + h.w / 2 - 6,
  ))
  return below.value
    ? { left: `${left}px`, top: `${Number.parseFloat(b.top) - 6}px` }
    : { left: `${left}px`, top: `${Number.parseFloat(b.top) + bubbleH.value - 6}px` }
})

function measure() {
  vw.value = window.innerWidth
  vh.value = window.innerHeight
  const el = current.value ? document.querySelector(current.value.sel) : null
  if (!el) {
    rect.value = null
    return
  }
  el.scrollIntoView({ block: 'center', behavior: 'smooth' })
  // 滚动是平滑的，等一帧再量，否则拿到的是滚动前的位置。
  window.setTimeout(() => {
    rect.value = el.getBoundingClientRect()
    nextTick(() => {
      bubbleH.value = bubbleRef.value?.offsetHeight || 120
    })
  }, 240)
}

function next() {
  if (index.value >= live.value.length - 1) return done()
  index.value += 1
  measure()
}

function done() {
  if (closing.value) return
  closing.value = true
  window.setTimeout(() => {
    emit('update:modelValue', false)
    emit('finish')
  }, 200)
}

function onResize() {
  if (props.modelValue) measure()
}

watch(
  () => props.modelValue,
  (open) => {
    if (open) {
      closing.value = false
      index.value = 0
      nextTick(measure)
    }
  },
)

onMounted(() => {
  window.addEventListener('resize', onResize)
  if (props.modelValue) nextTick(measure)
})
onUnmounted(() => window.removeEventListener('resize', onResize))
</script>

<template>
  <div v-if="modelValue && live.length" class="coach-root" :class="{ closing }">
    <!-- 蒙层：clip-path 挖洞，孔内是真的透明，能直接看到并操作底下的组件 -->
    <div class="coach-veil" :style="{ clipPath }" @click="next"></div>
    <div class="coach-ring g3" :style="ringStyle"></div>

    <!-- 小三角必须是气泡的兄弟节点：放进气泡里，top/left 会相对气泡计算而不是视口 -->
    <div class="coach-arrow" :style="arrowStyle"></div>

    <div v-if="current" ref="bubbleRef" class="coach-bubble" :style="bubbleStyle">
      <h3>{{ current.title }}</h3>
      <p>{{ current.body }}</p>
      <div class="coach-foot">
        <button class="coach-next" type="button" style="color:var(--secondary)" @click="done">跳过</button>
        <button class="coach-next" type="button" @click="next">
          {{ index >= total - 1 ? '知道了' : `下一步（${index + 1}/${total}）` }}
        </button>
      </div>
    </div>
  </div>
</template>
