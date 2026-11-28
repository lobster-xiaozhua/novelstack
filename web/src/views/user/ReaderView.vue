<script setup>
import { computed, nextTick, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { boot, bookById, getKeys, progressOf, recordProgress, flushProgress } from '../../user/store'
import { loadChapters } from '../../userApi'
import { unwrapDek } from '../../crypto/keys'
import * as idb from '../../offline/idb'

const route = useRoute()
const router = useRouter()
const bookId = String(route.params.id || '')

const book = ref(null)
const chapters = ref([])
const current = ref(0)
const text = ref('')
const loading = ref(true)
const error = ref('')
const offlineReading = ref(false)
const showToc = ref(false)
const speaking = ref(false)
const paused = ref(false)
const scroller = ref(null)

let worker = null
let seq = 0
const dekCache = new Map()
let saveTimer = null
let lastOffset = 0

const total = computed(() => chapters.value.length)
const percent = computed(() => {
  if (!total.value) return 0
  return Math.min(1, (current.value + 0.02) / total.value)
})

// 正文段落：换行切段 + 去掉与章节标题重复的首行 + 过滤空行。
// 标题已由上方 h2 显示，正文里再出现一遍就是"莫名其妙的重复标题"（见截图）。
// 只改渲染层，text.value 原样保留给 TTS 与搜索。
const paragraphs = computed(() => {
  const title = (chapters.value[current.value]?.title || '').trim()
  const out = []
  for (const raw of text.value.split('\n')) {
    let line = raw.trim()
    if (!line) continue
    // 整行就是标题（独立标题行）→ 丢弃，避免与 h2 重复
    if (title && (line === title || normalizeHeading(line) === normalizeHeading(title))) continue
    // 标题与正文粘连成一整行时，剥掉开头的标题前缀，正文不再重复标题
    if (title && line.startsWith(title) && line.length > title.length) {
      const rest = line.slice(title.length).trim()
      if (rest) line = rest
    }
    out.push(line)
  }
  return out
})

function normalizeHeading(s) {
  return s.replace(/[\s·•\-—:：\[\]【】]/g, '')
}

function ensureWorker() {
  if (!worker) {
    worker = new Worker(new URL('../../crypto/decrypt.worker.js', import.meta.url), { type: 'module' })
  }
  return worker
}

function decryptWithWorker(dek, items) {
  return new Promise((resolve, reject) => {
    const id = ++seq
    const w = ensureWorker()
    const onMsg = (ev) => {
      if (!ev.data || ev.data.id !== id) return
      w.removeEventListener('message', onMsg)
      if (ev.data.ok) resolve(ev.data.chapters)
      else reject(new Error(ev.data.error || '解密失败'))
    }
    w.addEventListener('message', onMsg)
    // dek 是 Uint8Array，结构化克隆会复制一份，主线程侧可继续复用。
    w.postMessage({ id, dek, bookId, items })
  })
}

async function dekFor() {
  if (dekCache.has(bookId)) return dekCache.get(bookId)
  if (!book.value?.wrapped_dek) throw new Error('缺少密钥包，请重新登录')
  const keys = await getKeys()
  const dek = await unwrapDek(keys, book.value.wrapped_dek)
  dekCache.set(bookId, dek)
  return dek
}

async function fetchCipher(index) {
  const cached = await idb.getCipherChapter(bookId, index).catch(() => null)
  const etag = chapters.value[index]?.etag || ''
  if (cached && (!etag || cached.etag === etag)) {
    return { ciphertext: cached.ciphertext, etag: cached.etag, cached: true }
  }
  const keys = await getKeys()
  const res = await loadChapters(bookId, [index], keys.pem)
  const item = (res.chapters || [])[0]
  if (!item) throw new Error('服务端未返回该章')
  await idb.putCipherChapter(bookId, index, item.ciphertext, item.etag).catch(() => {})
  return { ciphertext: item.ciphertext, etag: item.etag, cached: false }
}

async function openChapter(index, offset = 0) {
  if (!book.value) return
  if (index < 0 || (total.value && index >= total.value)) return
  stopSpeech()
  loading.value = true
  error.value = ''
  try {
    const dek = await dekFor()
    const cipher = await fetchCipher(index)
    const plain = await decryptWithWorker(dek, [{ index, ciphertext: cipher.ciphertext }])
    text.value = plain[0]?.text || ''
    current.value = index
    offlineReading.value = cipher.cached
    await nextTick()
    if (scroller.value) scroller.value.scrollTop = Math.max(0, offset || 0)
    lastOffset = Math.max(0, offset || 0)
    persist()
    // 预取后一章，保证翻页与离线连续阅读。
    prefetch(index + 1)
  } catch (err) {
    error.value = err.message || String(err)
    // 拉取失败但本地有密文时，退回缓存再试一次（离线场景）。
    try {
      const cached = await idb.getCipherChapter(bookId, index)
      if (cached) {
        const dek = await dekFor()
        const plain = await decryptWithWorker(dek, [{ index, ciphertext: cached.ciphertext }])
        text.value = plain[0]?.text || ''
        current.value = index
        offlineReading.value = true
        error.value = ''
      }
    } catch (_) { /* 保持错误提示 */ }
  } finally {
    loading.value = false
  }
}

async function prefetch(index) {
  if (!total.value || index >= total.value) return
  try {
    const cached = await idb.getCipherChapter(bookId, index)
    if (cached) return
    const keys = await getKeys()
    const res = await loadChapters(bookId, [index], keys.pem)
    for (const item of res.chapters || []) {
      await idb.putCipherChapter(bookId, index, item.ciphertext, item.etag)
    }
  } catch (_) { /* 预取失败不影响阅读 */ }
}

function persist() {
  if (saveTimer) clearTimeout(saveTimer)
  saveTimer = setTimeout(() => {
    recordProgress(bookId, current.value, lastOffset, percent.value)
  }, 600)
}

function onScroll() {
  if (!scroller.value) return
  lastOffset = Math.round(scroller.value.scrollTop)
  persist()
}

function next() {
  if (current.value + 1 < total.value) openChapter(current.value + 1, 0)
}

function prev() {
  if (current.value > 0) openChapter(current.value - 1, 0)
}

// ---------- TTS ----------
let ttsQueue = []
let ttsIndex = 0

function stopSpeech() {
  if (!('speechSynthesis' in window)) return
  window.speechSynthesis.cancel()
  speaking.value = false
  paused.value = false
  ttsQueue = []
  ttsIndex = 0
}

function speakFrom(start = 0) {
  if (!('speechSynthesis' in window)) {
    error.value = '当前浏览器不支持语音合成'
    return
  }
  stopSpeech()
  ttsQueue = text.value.split(/\n+/).map((s) => s.trim()).filter(Boolean)
  ttsIndex = start
  speaking.value = true
  speakNext()
}

function speakNext() {
  if (ttsIndex >= ttsQueue.length) {
    speaking.value = false
    return
  }
  const u = new SpeechSynthesisUtterance(ttsQueue[ttsIndex++])
  u.lang = 'zh-CN'
  u.rate = 1
  u.pitch = 1
  u.onend = () => {
    if (speaking.value) speakNext()
  }
  u.onerror = () => {
    speaking.value = false
  }
  window.speechSynthesis.speak(u)
}

function toggleSpeech() {
  if (!speaking.value) {
    const ratio = scroller.value && scroller.value.scrollHeight
      ? scroller.value.scrollTop / scroller.value.scrollHeight
      : 0
    speakFrom(Math.floor(ratio * 0.6 * (ttsQueue.length || 0)))
    return
  }
  if (paused.value) {
    window.speechSynthesis.resume()
    paused.value = false
  } else {
    window.speechSynthesis.pause()
    paused.value = true
  }
}

// ---------- 键盘与手势 ----------
function onKey(ev) {
  if (ev.key === 'ArrowRight' || ev.key === 'PageDown') next()
  if (ev.key === 'ArrowLeft' || ev.key === 'PageUp') prev()
  if (ev.key === 'Escape') {
    if (showToc.value) showToc.value = false
    else close()
  }
}

let touchX = 0
let touchY = 0
function onTouchStart(ev) {
  const t = ev.changedTouches[0]
  touchX = t.clientX
  touchY = t.clientY
}
function onTouchEnd(ev) {
  const t = ev.changedTouches[0]
  const dx = t.clientX - touchX
  const dy = t.clientY - touchY
  if (Math.abs(dx) > 60 && Math.abs(dx) > Math.abs(dy) * 1.6) {
    dx < 0 ? next() : prev()
  }
}

function close() {
  flushProgress()
  stopSpeech()
  router.replace('/library')
}

onMounted(async () => {
  await boot()
  book.value = bookById(bookId)
  if (!book.value) {
    error.value = '找不到这本书，可能已被删除'
    loading.value = false
    return
  }
  chapters.value = book.value.chapters || []
  const p = progressOf(bookId)
  await openChapter(p?.chapter_index ?? 0, p?.char_offset ?? 0)
  window.addEventListener('keydown', onKey)
})

onUnmounted(() => {
  window.removeEventListener('keydown', onKey)
  stopSpeech()
  flushProgress()
  if (worker) worker.terminate()
})
</script>

<template>
  <div class="reader">
    <div class="reader-bar">
      <button @click="close">‹ 书架</button>
      <div class="title ellip">{{ chapters[current]?.title || book?.title || '' }}</div>
      <button @click="showToc = true">目录</button>
      <button @click="toggleSpeech">{{ speaking ? (paused ? '继续' : '暂停') : '朗读' }}</button>
    </div>

    <div
      ref="scroller"
      class="reader-body"
      @scroll.passive="onScroll"
      @touchstart.passive="onTouchStart"
      @touchend.passive="onTouchEnd"
    >
      <h2>{{ chapters[current]?.title || '' }}</h2>
      <div v-if="loading" class="skeleton sk-line" style="width:70%"></div>
      <div v-if="loading" class="skeleton sk-line" style="width:92%;margin-top:14px"></div>
      <div v-if="loading" class="skeleton sk-line" style="width:88%"></div>
      <div v-if="loading" class="skeleton sk-line" style="width:95%"></div>
      <div v-if="error" class="error">{{ error }}</div>
      <div v-if="offlineReading && !loading" class="note" style="font-size:12px">
        离线阅读：本章来自本地密文缓存
      </div>
      <div v-if="!loading" class="reader-text">
        <p v-for="(para, i) in paragraphs" :key="i">{{ para }}</p>
        <div v-if="!paragraphs.length" class="empty" style="padding:24px">本章暂无正文</div>
      </div>
    </div>

    <div class="reader-foot">
      <button class="btn ghost" :disabled="current === 0" @click="prev">上一章</button>
      <div class="progress"><i :style="{ width: (percent * 100) + '%' }"></i></div>
      <button class="btn ghost" :disabled="current + 1 >= total" @click="next">下一章</button>
    </div>

    <div v-if="showToc" class="overlay" @click.self="showToc = false">
      <div class="modal" style="max-height:76vh;overflow:auto">
        <h2>目录</h2>
        <div class="rows">
          <button
            v-for="c in chapters"
            :key="c.index"
            class="row"
            style="width:100%;background:none;border:0;cursor:pointer;text-align:left"
            @click="showToc = false; openChapter(c.index, 0)"
          >
            <div class="label ellip" :style="{ color: c.index === current ? 'var(--blue)' : 'var(--label)' }">
              {{ c.title }}
            </div>
            <div class="dim" style="font-size:12px">{{ c.chars ? c.chars + ' 字' : '' }}</div>
          </button>
        </div>
        <div class="modal-actions">
          <button class="btn ghost" @click="showToc = false">关闭</button>
        </div>
      </div>
    </div>
  </div>
</template>
