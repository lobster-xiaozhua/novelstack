<script setup>
import { computed, onMounted, ref } from 'vue'
import { getBooks, uploadBook } from '../api'
import { bytes, ts } from '../format'

// 管理端书籍管理：上传入口 + 书籍列表。
// 安全边界：服务端只接受 .txt 且流式加密；前端只做 File 投递与交互，
// 不读文件正文、不做任何客户端加密（DEK 由服务端生成并信封封存）。

const MAX_BYTES = 50 * 1024 * 1024 // 与服务端 MAX_UPLOAD_MB=50 对齐

const books = ref([])
const loading = ref(true)
const listError = ref('')

// 上传弹窗状态
const open = ref(false)
const dragOver = ref(false)
const file = ref(null) // { name, size }
const title = ref('')
const author = ref('')
const tags = ref('')
const description = ref('')
const phase = ref('idle') // idle | uploading | done | error
const progress = ref(0)
const uploadError = ref('')
const fileInput = ref(null)

// 轻提示（Toast）：超限等非阻塞错误
const toast = ref('')
let toastTimer = 0
function showToast(msg) {
  toast.value = msg
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => { toast.value = '' }, 3200)
}

async function loadBooks() {
  loading.value = true
  listError.value = ''
  try {
    books.value = await getBooks()
  } catch (e) {
    listError.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

onMounted(loadBooks)

function resetForm() {
  file.value = null
  title.value = ''
  author.value = ''
  tags.value = ''
  description.value = ''
  phase.value = 'idle'
  progress.value = 0
  uploadError.value = ''
  dragOver.value = false
  if (fileInput.value) fileInput.value.value = ''
}

function openModal() {
  resetForm()
  open.value = true
}

function closeModal() {
  if (phase.value === 'uploading') return // 上传中不允许误关
  open.value = false
}

function acceptFile(f) {
  if (!f) return
  uploadError.value = ''
  phase.value = 'idle'
  progress.value = 0
  const name = f.name || ''
  if (!/\.txt$/i.test(name)) {
    showToast('仅支持 .txt 纯文本；EPUB 等格式服务端不解析')
    return
  }
  if (f.size > MAX_BYTES) {
    showToast(`文件 ${bytes(f.size)} 超过 50MB 上限，请拆分后再传`)
    return
  }
  if (f.size === 0) {
    showToast('空文件无法上传')
    return
  }
  file.value = f
  if (!title.value) title.value = name.replace(/\.txt$/i, '')
}

function onPick(e) {
  acceptFile(e.target.files && e.target.files[0])
}

function onDrop(e) {
  e.preventDefault()
  dragOver.value = false
  acceptFile(e.dataTransfer.files && e.dataTransfer.files[0])
}

function onDragOver(e) {
  e.preventDefault()
  dragOver.value = true
}

function onDragLeave(e) {
  e.preventDefault()
  dragOver.value = false
}

const canSubmit = computed(() => !!file.value && !!title.value.trim() && phase.value !== 'uploading')

async function submit() {
  if (!canSubmit.value) return
  phase.value = 'uploading'
  progress.value = 0
  uploadError.value = ''
  try {
    await uploadBook({
      file: file.value,
      title: title.value.trim(),
      author: author.value.trim(),
      tags: tags.value.trim(),
      description: description.value.trim(),
      onProgress: (p) => { progress.value = Math.min(1, p) },
    })
    phase.value = 'done'
    progress.value = 1
    await loadBooks() // 成功后刷新书籍列表
  } catch (e) {
    phase.value = 'error'
    uploadError.value = e.message || String(e)
  }
}

const pct = computed(() => Math.round(progress.value * 100))
</script>

<template>
  <div>
    <header class="top">
      <div>
        <h1>书籍管理</h1>
        <div class="sub">上传与查看书库 · 明文不落盘，服务端流式信封加密</div>
      </div>
      <div class="actions">
        <button class="btn" type="button" @click="openModal">
          <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
            <path d="M7 2.5v9M2.5 7h9" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" />
          </svg>
          上传书籍
        </button>
        <button class="btn ghost" type="button" :disabled="loading" @click="loadBooks">刷新</button>
      </div>
    </header>

    <div v-if="toast" class="up-toast" role="status">{{ toast }}</div>

    <div v-if="listError" class="error">{{ listError }}</div>
    <div v-else-if="loading" class="loading">加载书籍…</div>

    <div v-else class="card" style="padding:0;overflow:hidden">
      <div v-if="!books.length" class="empty" style="padding:36px">书库还是空的，点右上角「上传书籍」导入第一本。</div>
      <table v-else class="tbl">
        <thead>
          <tr>
            <th>书名</th>
            <th>作者</th>
            <th class="num">章节</th>
            <th class="num">大小</th>
            <th>状态</th>
            <th>导入时间</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="b in books" :key="b.id">
            <td data-label="书名" class="ellip"><b>{{ b.title }}</b><div class="hint">{{ b.orig_filename }}</div></td>
            <td data-label="作者">{{ b.author || '—' }}</td>
            <td class="num" data-label="章节">{{ b.chapter_count }}</td>
            <td class="num" data-label="大小">{{ bytes(b.size_bytes) }}</td>
            <td data-label="状态"><span class="pill" :class="b.index_state === 'ok' ? 'ok' : 'muted'">{{ b.index_state }}</span></td>
            <td data-label="导入时间">{{ ts(b.created_at) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- 上传弹窗 -->
    <div v-if="open" class="overlay" @mousedown.self="closeModal">
      <div class="modal up-modal" role="dialog" aria-modal="true" aria-label="上传书籍">
        <h2>上传书籍</h2>
        <p class="up-lead">仅支持 .txt 纯文本，单本不超过 50MB。上传即流式加密，明文不落盘。</p>

        <!-- 拖拽 / 选择 -->
        <div
          class="up-zone"
          :class="{ over: dragOver, has: !!file, disabled: phase === 'uploading' }"
          @dragover="onDragOver"
          @dragleave="onDragLeave"
          @drop="onDrop"
          @click="phase !== 'uploading' && fileInput && fileInput.click()"
        >
          <input ref="fileInput" type="file" accept=".txt,text/plain" hidden @change="onPick" />
          <template v-if="!file">
            <svg width="30" height="30" viewBox="0 0 30 30" fill="none" aria-hidden="true">
              <path d="M15 20V7m0 0-5 5m5-5 5 5M6 20v3h18v-3" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
            <div class="up-zone-t">拖拽 .txt 到这里，或点击选择</div>
            <div class="up-zone-s">UTF-8 编码 · 自动按章节标题切分</div>
          </template>
          <template v-else>
            <svg width="26" height="26" viewBox="0 0 26 26" fill="none" aria-hidden="true">
              <path d="M6 3h10l4 4v16H6z" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" />
              <path d="M16 3v4h4" stroke="currentColor" stroke-width="1.6" stroke-linejoin="round" />
            </svg>
            <div class="up-zone-t ellip">{{ file.name }}</div>
            <div class="up-zone-s">{{ bytes(file.size) }} · 点击可更换</div>
          </template>
        </div>

        <!-- 元数据 -->
        <div class="up-fields">
          <label class="field">
            <span>书名</span>
            <input v-model="title" type="text" maxlength="120" placeholder="必填" :disabled="phase === 'uploading'" />
          </label>
          <label class="field">
            <span>作者</span>
            <input v-model="author" type="text" maxlength="120" placeholder="选填" :disabled="phase === 'uploading'" />
          </label>
          <label class="field">
            <span>标签</span>
            <input v-model="tags" type="text" maxlength="200" placeholder="选填，逗号分隔" :disabled="phase === 'uploading'" />
          </label>
        </div>

        <!-- 进度 / 结果 -->
        <div v-if="phase !== 'idle'" class="up-status" :class="phase">
          <div class="up-status-head">
            <span class="up-status-icon">
              <template v-if="phase === 'done'">
                <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                  <path d="m3 7.2 2.6 2.6L11 4.4" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
                </svg>
              </template>
              <template v-else-if="phase === 'error'">!</template>
              <template v-else>{{ pct }}%</template>
            </span>
            <span class="up-status-text ellip">
              {{ phase === 'uploading'
                 ? `正在上传 ${file ? file.name : ''} · ${pct}%`
                 : (phase === 'done' ? '上传成功，已加入书库' : `上传失败 · ${file ? file.name : ''}`) }}
            </span>
          </div>
          <div class="up-track"><i :style="{ width: pct + '%' }"></i></div>
          <div v-if="phase === 'error'" class="up-err">{{ uploadError }}</div>
        </div>

        <div class="modal-actions">
          <button class="btn ghost" type="button" :disabled="phase === 'uploading'" @click="closeModal">
            {{ phase === 'done' ? '完成' : '取消' }}
          </button>
          <button v-if="phase !== 'done'" class="btn" type="button" :disabled="!canSubmit" @click="submit">
            {{ phase === 'error' ? '重试' : (phase === 'uploading' ? '上传中…' : '开始上传') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
