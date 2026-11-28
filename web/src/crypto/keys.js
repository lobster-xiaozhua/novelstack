// 浏览器端密钥与会话管理。
//
// 安全模型（与移动端同构）：
//  1. 每个标签页生成一对 RSA-OAEP-256 临时密钥，私钥只写 sessionStorage；
//  2. 服务端用公钥包装书籍 DEK，浏览器用私钥解包得到 DEK（32 字节）；
//  3. DEK 只存在内存里，不落 localStorage / IndexedDB；
//  4. 关闭标签页 → sessionStorage 清空 → 缓存密文无法再解，符合「关页即失效」。
//
// 为什么不是「密码派生 KEK」：服务端从不接触用户密码（只存哈希），
// 无法用密码派生同一把 KEK；用设备密钥包装 DEK 是唯一不引入明文密钥传输的做法。

const PRIV_KEY = 'novelstack.web.kp'
const ALG = { name: 'RSA-OAEP', hash: 'SHA-256' }
const B64 = /^[A-Za-z0-9+/=]+$/

function b64ToBytes(b64) {
  const bin = atob(b64)
  const out = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i)
  return out
}

function bytesToB64(bytes) {
  let bin = ''
  const chunk = 0x8000
  for (let i = 0; i < bytes.length; i += chunk) {
    bin += String.fromCharCode.apply(null, bytes.subarray(i, i + chunk))
  }
  return btoa(bin)
}

function derToPem(der) {
  const b64 = bytesToB64(new Uint8Array(der))
  const lines = b64.match(/.{1,64}/g) || []
  return `-----BEGIN PUBLIC KEY-----\n${lines.join('\n')}\n-----END PUBLIC KEY-----\n`
}

/**
 * 取得（必要时生成）本标签页的设备密钥对。
 *
 * 刷新页面后从 sessionStorage 恢复：私钥以 non-extractable 导入（内存里导不出去），
 * 公钥单独存一份 JWK 再以可导出方式导入——否则 `exportKey('spki')` 会抛
 * 「key is not extractable」，整个 boot 直接失败（曾经的线上级 bug）。
 * 公钥本来就是发给服务端的东西，落 sessionStorage 没有保密问题。
 */
export async function ensureDeviceKeys() {
  const cached = sessionStorage.getItem(PRIV_KEY)
  if (cached) {
    try {
      const saved = JSON.parse(cached)
      if (saved && saved.priv && saved.pub) {
        const privateKey = await crypto.subtle.importKey('jwk', saved.priv, ALG, false, ['decrypt'])
        const publicKey = await crypto.subtle.importKey('jwk', saved.pub, ALG, true, ['encrypt'])
        return { privateKey, publicKey }
      }
    } catch (_) {
      // 旧格式（只存了私钥）或内容损坏：直接重建，服务端会按新公钥重新包装 DEK。
    }
    sessionStorage.removeItem(PRIV_KEY)
  }
  const pair = await crypto.subtle.generateKey(
    {
      name: 'RSA-OAEP',
      modulusLength: 2048,
      publicExponent: new Uint8Array([1, 0, 1]),
      hash: 'SHA-256',
    },
    true,
    ['encrypt', 'decrypt'],
  )
  const priv = await crypto.subtle.exportKey('jwk', pair.privateKey)
  const pub = await crypto.subtle.exportKey('jwk', pair.publicKey)
  sessionStorage.setItem(PRIV_KEY, JSON.stringify({ priv, pub }))
  return { privateKey: pair.privateKey, publicKey: pair.publicKey }
}

/** 导出 SPKI PEM（服务端用 devices.ParseRSAPublicKey 解析）。 */
export async function exportPublicKeyPem(keys) {
  if (!keys || !keys.publicKey) throw new Error('缺少公钥：请通过 ensureDeviceKeys() 取得密钥对')
  const spki = await crypto.subtle.exportKey('spki', keys.publicKey)
  return derToPem(spki)
}

/** 用私钥解包服务端给的 base64 RSA-OAEP 密文，得到 32 字节 DEK。 */
export async function unwrapDek(keys, wrappedB64) {
  if (!wrappedB64 || !B64.test(wrappedB64)) throw new Error('无效的密钥包')
  const ct = b64ToBytes(wrappedB64)
  const raw = await crypto.subtle.decrypt(
    { name: 'RSA-OAEP' },
    keys.privateKey,
    ct,
  )
  if (raw.byteLength !== 32) throw new Error('DEK 长度异常')
  return new Uint8Array(raw)
}

/** 会话内是否还有私钥（关闭标签页后为 false）。 */
export function hasSessionKeys() {
  return !!sessionStorage.getItem(PRIV_KEY)
}

export { b64ToBytes, bytesToB64 }
