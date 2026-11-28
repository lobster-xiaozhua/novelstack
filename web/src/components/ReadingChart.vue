<script setup>
// 30 天阅读柱状图（ECharts 按需引入）。
//
// 只注册 BarChart + Grid + Tooltip + CanvasRenderer，ECharts 主包不进首屏：
// 该组件通过 defineAsyncComponent / 路由懒加载后才拉取 echarts chunk。
import { onMounted, onUnmounted, ref, watch } from 'vue'

const props = defineProps({
  days: { type: Array, default: () => [] },
  height: { type: Number, default: 260 },
})

const el = ref(null)
let chart = null
let observer = null

function human(seconds) {
  const s = Math.max(0, Math.round(seconds || 0))
  if (s < 60) return `${s} 秒`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} 分钟`
  const h = Math.floor(m / 60)
  const rest = m % 60
  return rest ? `${h} 小时 ${rest} 分` : `${h} 小时`
}

function shortDay(day) {
  // 2026-11-28 → 11/28
  return day && day.length >= 10 ? `${day.slice(5, 7)}/${day.slice(8, 10)}` : day
}

async function mount() {
  if (!el.value) return
  const [{ init, use }, { BarChart }, { GridComponent, TooltipComponent }, { CanvasRenderer }] =
    await Promise.all([
      import('echarts/core'),
      import('echarts/charts'),
      import('echarts/components'),
      import('echarts/renderers'),
    ])
  use([BarChart, GridComponent, TooltipComponent, CanvasRenderer])
  chart = init(el.value, null, { renderer: 'canvas' })
  render()
  observer = new ResizeObserver(() => chart && chart.resize())
  observer.observe(el.value)
}

function render() {
  if (!chart) return
  const data = props.days || []
  chart.setOption({
    animationDuration: 420,
    animationEasing: 'cubicOut',
    grid: { left: 44, right: 12, top: 16, bottom: 28 },
    tooltip: {
      trigger: 'axis',
      backgroundColor: '#FFFFFF',
      borderColor: 'rgba(60,60,67,.12)',
      borderWidth: 0.5,
      textStyle: { color: '#1C1C1E', fontSize: 12 },
      axisPointer: { type: 'shadow', shadowStyle: { color: 'rgba(120,120,128,.06)' } },
      formatter: (params) => {
        const p = params[0]
        return `${p.axisValue}<br/>${human(p.value)}`
      },
    },
    xAxis: {
      type: 'category',
      data: data.map((d) => shortDay(d.day)),
      axisLine: { lineStyle: { color: 'rgba(60,60,67,.12)' } },
      axisTick: { show: false },
      axisLabel: { color: '#8E8E93', fontSize: 10.5, interval: 'auto' },
    },
    yAxis: {
      type: 'value',
      splitLine: { lineStyle: { color: 'rgba(60,60,67,.08)' } },
      axisLabel: {
        color: '#8E8E93',
        fontSize: 10.5,
        formatter: (v) => (v >= 3600 ? `${Math.round(v / 3600)}h` : `${Math.round(v / 60)}m`),
      },
    },
    series: [
      {
        type: 'bar',
        data: data.map((d) => d.seconds || 0),
        barMaxWidth: 14,
        itemStyle: { color: '#007AFF', borderRadius: [4, 4, 0, 0] },
        emphasis: { itemStyle: { color: '#0A84FF' } },
      },
    ],
  })
}

onMounted(mount)
watch(() => props.days, render, { deep: true })
onUnmounted(() => {
  if (observer) observer.disconnect()
  if (chart) { chart.dispose(); chart = null }
})
</script>

<template>
  <div ref="el" :style="{ width: '100%', height: height + 'px' }"></div>
</template>
