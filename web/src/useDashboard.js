import { onMounted, onUnmounted, ref } from 'vue'
import { getDashboard } from './api'

// 所有页面共用一次聚合请求，避免前端多次轮询。
export function useDashboard(intervalMs = 15000) {
  const data = ref(null)
  const error = ref('')
  const loading = ref(true)
  const updatedAt = ref('')
  let timer = null
  let stopped = false

  async function load() {
    try {
      data.value = await getDashboard()
      updatedAt.value = new Date().toISOString()
      error.value = ''
    } catch (e) {
      if (!stopped) error.value = e.message || String(e)
    } finally {
      loading.value = false
    }
  }

  onMounted(() => {
    load()
    timer = setInterval(load, intervalMs)
  })
  onUnmounted(() => {
    stopped = true
    if (timer) clearInterval(timer)
  })

  return { data, error, loading, updatedAt, load }
}
