// NSCH 章节包解密（与 Go 的 internal/chapterpkg 逐字节对齐）。
//
// 布局：
//   0  "NSCH" | 4 version=1 | 5 reserved | 6 flags u16le | 8 index u32le
//   12 plainLen u32le | 16 nonce(12) | 28 ciphertext+tag(16)
// AAD = "novelstack-mobile-chapter-v1|" + bookID(UTF-8) + "|" + header[0:16]
//
// 头部全部纳入认证：任何篡改（章节号、长度）都会让 AES-GCM 校验失败。

const MAGIC = 0x4e534348 // "NSCH"
const HEADER = 28
const TAG = 16
const PREFIX = new TextEncoder().encode('novelstack-mobile-chapter-v1|')
const SEP = new Uint8Array([0x7c]) // '|'

let cachedKey = null
let cachedDek = null

/** 把原始 DEK 导入为 AES-GCM 密钥（按 DEK 缓存，避免每章重复导入）。 */
export async function importDek(dek) {
  if (cachedKey && cachedDek && sameBytes(cachedDek, dek)) return cachedKey
  const key = await crypto.subtle.importKey('raw', dek, { name: 'AES-GCM' }, false, ['decrypt'])
  cachedKey = key
  cachedDek = dek.slice()
  return key
}

/** 解密单章。pkgBase64 是服务端返回的 ciphertext（base64）。 */
export async function openChapter(dek, bookId, index, pkgBase64) {
  const pkg = base64ToBytes(pkgBase64)
  if (pkg.byteLength < HEADER + TAG) throw new Error('章节包过短')
  const view = new DataView(pkg.buffer, pkg.byteOffset, pkg.byteLength)
  if (view.getUint32(0, false) !== MAGIC) throw new Error('章节包标识不匹配')
  if (view.getUint8(4) !== 1) throw new Error('章节包版本不支持')
  if (view.getUint32(8, true) !== index) throw new Error('章节号不匹配')
  const plainLen = view.getUint32(12, true)

  const nonce = pkg.subarray(16, 28)
  const ct = pkg.subarray(HEADER)
  const aad = concat(PREFIX, new TextEncoder().encode(bookId), SEP, pkg.subarray(0, 16))

  const key = await importDek(dek)
  const plain = await crypto.subtle.decrypt(
    { name: 'AES-GCM', iv: nonce, additionalData: aad, tagLength: 128 },
    key,
    ct,
  )
  if (plain.byteLength !== plainLen) throw new Error('章节长度校验失败')
  return new TextDecoder('utf-8').decode(plain)
}

function concat(...parts) {
  const len = parts.reduce((n, p) => n + p.byteLength, 0)
  const out = new Uint8Array(len)
  let off = 0
  for (const p of parts) {
    out.set(p, off)
    off += p.byteLength
  }
  return out
}

function base64ToBytes(b64) {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

function sameBytes(a, b) {
  if (a.byteLength !== b.byteLength) return false
  for (let i = 0; i < a.byteLength; i++) if (a[i] !== b[i]) return false
  return true
}
