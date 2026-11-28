// 用户端共享状态：书架、进度、密钥。
//
// 模块级单例（私人站单用户，不需要 Pinia）：路由切换不重复拉库，
// 但每次刷新都重新生成会话密钥并要求服务端重新包装 DEK。

import { reactive } from 'vue'
import { ensureDeviceKeys, exportPublicKeyPem } from '../crypto/keys'
import { loadLibrary, pushProgress, pullProgress } from '../userApi'
import * as idb from '../offline/idb'

export const state = reactive({
  ready: false,
  loading: false,
  error: '',
  offline: false,
  books: [],
  progress: {},      // bookId -> { chapter_index, char_offset, percent, updated_at }
  cursor: '',
  lastSync: 0,
})

let keys = null
let booting = null

/** 取得本会话密钥（含公钥 PEM）。 */
export async function getKeys() {
  if (!keys) {
    keys = await ensureDeviceKeys()
    keys.pem = await exportPublicKeyPem(keys)
  }
  return keys
}

export function bookById(id) {
  return state.books.find((b) => b.id === id) || null
}

export function progressOf(id) {
  return state.progress[id] || null
}

function indexLibrary(lib) {
  const manifest = lib.manifest || {}
  state.books = (manifest.books || []).map((b) => ({ ...b }))
  const map = {}
  for (const p of lib.progress || []) map[p.book_id] = p
  state.progress = map
  state.cursor = lib.progress_cursor || ''
  state.lastSync = Date.now()
  // 元数据也缓存一份：断网时书架仍可见（正文另由密文缓存决定）。
  for (const b of state.books) idb.putBookMeta(b).catch(() => {})
}

/** 加载书架；失败时回退到本地缓存（离线模式）。 */
export async function boot(force = false) {
  if (state.ready && !force) return
  if (booting) return booting
  state.loading = true
  state.error = ''
  booting = (async () => {
    try {
      const k = await getKeys()
      const lib = await loadLibrary(k.pem)
      indexLibrary(lib)
      state.offline = false
      state.ready = true
    } catch (err) {
      // 离线：用 IndexedDB 里的元数据兜底，至少能进已缓存的章节。
      try {
        const metas = await idb.listBookMeta()
        const progresses = await idb.listLocalProgress()
        if (metas.length) {
          state.books = metas.map((m) => ({
            id: m.id, title: m.title, author: m.author,
            chapter_count: m.chapter_count, chapters: m.chapters || [],
            wrapped_dek: m.wrapped_dek || '', offline: true,
          }))
          const map = {}
          for (const p of progresses) {
            map[p.bookId] = {
              book_id: p.bookId, chapter_index: p.chapterIndex,
              char_offset: p.charOffset, percent: p.percent,
            }
          }
          state.progress = map
          state.offline = true
          state.ready = true
        } else {
          state.error = err.message || String(err)
        }
      } catch (_) {
        state.error = err.message || String(err)
      }
    } finally {
      state.loading = false
      booting = null
    }
  })()
  return booting
}

/** 拉增量进度（5 分钟一次 / 回到前台时）。 */
export async function refreshProgress() {
  try {
    const res = await pullProgress(state.cursor || '')
    for (const p of res.items || []) {
      const cur = state.progress[p.book_id]
      if (!cur || ahead(p, cur)) {
        state.progress[p.book_id] = p
        await idb.putLocalProgress(p.book_id, p.chapter_index, p.char_offset, p.percent)
      }
    }
    if (res.cursor) state.cursor = res.cursor
    state.lastSync = Date.now()
    state.offline = false
  } catch (_) {
    state.offline = true
  }
}

function ahead(a, b) {
  if (a.chapter_index !== b.chapter_index) return a.chapter_index > b.chapter_index
  return a.char_offset > b.char_offset
}

/** 记录进度：本地立即生效，后台按 debounce 上报。 */
let pushTimer = null
let pending = new Map()

export function recordProgress(bookId, chapterIndex, charOffset, percent) {
  state.progress[bookId] = {
    book_id: bookId, chapter_index: chapterIndex, char_offset: charOffset,
    percent, updated_at: new Date().toISOString(),
  }
  idb.putLocalProgress(bookId, chapterIndex, charOffset, percent).catch(() => {})
  pending.set(bookId, {
    book_id: bookId, chapter_index: chapterIndex, char_offset: charOffset, percent,
  })
  if (pushTimer) clearTimeout(pushTimer)
  pushTimer = setTimeout(flushProgress, 1200)
}

export async function flushProgress() {
  if (!pending.size) return
  const items = [...pending.values()]
  pending = new Map()
  try {
    const res = await pushProgress(items)
    // 服务端按位置合并，可能保留更靠后的记录——以服务端为准回写。
    for (const p of res.items || []) {
      const cur = state.progress[p.book_id]
      if (!cur || ahead(p, cur)) state.progress[p.book_id] = p
    }
    if (res.cursor) state.cursor = res.cursor
    state.offline = false
  } catch (_) {
    // 离线：保留本地，下次 flush 或 refresh 时再对齐。
    for (const it of items) {
      if (!pending.has(it.book_id)) pending.set(it.book_id, it)
    }
    state.offline = true
  }
}

/** 退出登录：清缓存与密钥（sessionStorage 随标签页关闭也会清）。 */
export async function clearLocal() {
  await idb.clearAll().catch(() => {})
  sessionStorage.removeItem('novelstack.web.kp')
  keys = null
  state.ready = false
  state.books = []
  state.progress = {}
}
