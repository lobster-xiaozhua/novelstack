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

/** 取得（必要时生成）本标签页的设备密钥对。 */
export async function ensureDeviceKeys() {
  const cached = sessionStorage.getItem(PRIV_KEY)
  if (cached) {
    try {
      const jwk = JSON.parse(cached)
      const privateKey = await crypto.subtle.importKey(
        'jwk', jwk,
        { name: 'RSA-OAEP', hash: 'SHA-256' },
        false, ['decrypt'],
      )
      return { privateKey }
    } catch (_) {
      sessionStorage.removeItem(PRIV_KEY)
    }
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
  const jwk = await crypto.subtle.exportKey('jwk', pair.privateKey)
  sessionStorage.setItem(PRIV_KEY, JSON.stringify(jwk))
  return { privateKey: pair.privateKey, publicKey: pair.publicKey }
}

/** 导出 SPKI PEM（服务端用 devices.ParseRSAPublicKey 解析）。 */
export async function exportPublicKeyPem(keys) {
  const pub = keys.publicKey || (await publicKeyFromPrivate(keys.privateKey))
  const spki = await crypto.subtle.exportKey('spki', pub)
  return derToPem(spki)
}

async function publicKeyFromPrivate(privateKey) {
  const jwk = await crypto.subtle.exportKey('jwk', privateKey)
  delete jwk.d
  delete jwk.p
  delete jwk.q
  delete jwk.dp
  delete jwk.dq
  delete jwk.qi
  jwk.key_ops = ['encrypt']
  return crypto.subtle.importKey('jwk', jwk, { name: 'RSA-OAEP', hash: 'SHA-256' }, true, ['encrypt'])
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
