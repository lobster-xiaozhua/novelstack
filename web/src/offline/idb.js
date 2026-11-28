// IndexedDB：离线缓存密文与进度。
//
// 只存密文（NSCH）与元数据，不存明文、不存 DEK、不存私钥。
// 断网刷新时：SW 提供页面，页面用 sessionStorage 里的私钥解 wrapped_dek，
// 再解 IDB 里的密文——因此「关标签页」后缓存不可读（预期行为）。

const DB_NAME = 'novelstack'
const DB_VERSION = 1
const STORE_CHAPTERS = 'chapters'
const STORE_META = 'meta'
const STORE_PROGRESS = 'progress'

let dbPromise = null

function openDb() {
  if (dbPromise) return dbPromise
  dbPromise = new Promise((resolve, reject) => {
    const req = indexedDB.open(DB_NAME, DB_VERSION)
    req.onupgradeneeded = () => {
      const db = req.result
      if (!db.objectStoreNames.contains(STORE_CHAPTERS)) {
        const s = db.createObjectStore(STORE_CHAPTERS, { keyPath: 'key' })
        s.createIndex('book', 'bookId', { unique: false })
      }
      if (!db.objectStoreNames.contains(STORE_META)) {
        db.createObjectStore(STORE_META, { keyPath: 'key' })
      }
      if (!db.objectStoreNames.contains(STORE_PROGRESS)) {
        db.createObjectStore(STORE_PROGRESS, { keyPath: 'bookId' })
      }
    }
    req.onsuccess = () => resolve(req.result)
    req.onerror = () => reject(req.error)
  })
  return dbPromise
}

function tx(store, mode, fn) {
  return openDb().then((db) => new Promise((resolve, reject) => {
    const t = db.transaction(store, mode)
    const os = t.objectStore(store)
    let out
    try {
      out = fn(os)
    } catch (err) {
      reject(err)
      return
    }
    t.oncomplete = () => resolve(out && out.result !== undefined ? out.result : out)
    t.onerror = () => reject(t.error)
    t.onabort = () => reject(t.error)
  }))
}

const chapterKey = (bookId, index) => `${bookId}:${index}`

/** 缓存一章密文。 */
export function putCipherChapter(bookId, index, ciphertext, etag = '') {
  return tx(STORE_CHAPTERS, 'readwrite', (os) =>
    os.put({ key: chapterKey(bookId, index), bookId, index, ciphertext, etag, at: Date.now() }),
  )
}

/** 读取一章密文（不存在返回 null）。 */
export async function getCipherChapter(bookId, index) {
  const row = await tx(STORE_CHAPTERS, 'readonly', (os) => os.get(chapterKey(bookId, index)))
  return row || null
}

/** 某本书已缓存的密文章节（用于离线目录）。 */
export async function listCachedIndexes(bookId) {
  const db = await openDb()
  return new Promise((resolve, reject) => {
    const t = db.transaction(STORE_CHAPTERS, 'readonly')
    const idx = t.objectStore(STORE_CHAPTERS).index('book')
    const req = idx.getAll(IDBKeyRange.only(bookId))
    req.onsuccess = () => resolve((req.result || []).map((r) => r.index).sort((a, b) => a - b))
    req.onerror = () => reject(req.error)
  })
}

/** 缓存整本书的密钥包（wrapped DEK）与元数据，供离线解包。 */
export function putBookMeta(book) {
  return tx(STORE_META, 'readwrite', (os) =>
    os.put({
      key: `book:${book.id}`,
      id: book.id,
      title: book.title,
      author: book.author || '',
      chapter_count: book.chapter_count || 0,
      wrapped_dek: book.wrapped_dek || '',
      chapters: book.chapters || [],
      at: Date.now(),
    }),
  )
}

export async function getBookMeta(bookId) {
  const row = await tx(STORE_META, 'readonly', (os) => os.get(`book:${bookId}`))
  return row || null
}

export async function listBookMeta() {
  const rows = await tx(STORE_META, 'readonly', (os) => os.getAll())
  return (rows || []).filter((r) => r.key && r.key.startsWith('book:'))
}

/** 本地进度（离线续读）。 */
export function putLocalProgress(bookId, chapterIndex, charOffset, percent) {
  return tx(STORE_PROGRESS, 'readwrite', (os) =>
    os.put({ bookId, chapterIndex, charOffset, percent, at: Date.now() }),
  )
}

export async function getLocalProgress(bookId) {
  const row = await tx(STORE_PROGRESS, 'readonly', (os) => os.get(bookId))
  return row || null
}

export async function listLocalProgress() {
  const rows = await tx(STORE_PROGRESS, 'readonly', (os) => os.getAll())
  return rows || []
}

/** 清理：退出登录或票据失效时调用，避免残留密文。 */
export async function clearAll() {
  const db = await openDb()
  await Promise.all([STORE_CHAPTERS, STORE_META, STORE_PROGRESS].map((name) =>
    new Promise((resolve, reject) => {
      const t = db.transaction(name, 'readwrite')
      t.objectStore(name).clear()
      t.oncomplete = resolve
      t.onerror = () => reject(t.error)
    }),
  ))
}
