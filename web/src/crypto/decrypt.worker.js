// 章节解密 Worker：把 AES-GCM 解包放到后台线程，长章节不阻塞滚动与输入。
import { openChapter } from './nsch.js'

self.onmessage = async (event) => {
  const { id, dek, bookId, items } = event.data || {}
  try {
    const chapters = []
    for (const item of items || []) {
      const text = await openChapter(dek, bookId, item.index, item.ciphertext)
      chapters.push({ index: item.index, text })
    }
    self.postMessage({ id, ok: true, chapters })
  } catch (err) {
    self.postMessage({ id, ok: false, error: err && err.message ? err.message : String(err) })
  }
}
