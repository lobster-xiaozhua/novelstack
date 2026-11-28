// 用户端 API：书架、章节密文、进度同步、会话。
//
// 所有请求同源带 cookie；401 统一回 /login 并带回跳地址，
// 不区分「会话过期」与「未登录」——私人站只有一个用户。

function loginUrl() {
  const next = location.pathname + location.search
  return '/login?next=' + encodeURIComponent(next)
}

async function request(path, options = {}) {
  const res = await fetch(path, {
    credentials: 'same-origin',
    headers: options.body ? { 'Content-Type': 'application/json' } : {},
    ...options,
  })
  if (res.status === 401) {
    location.replace(loginUrl())
    throw new Error('未登录')
  }
  if (!res.ok) {
    let msg = `HTTP ${res.status}`
    try {
      const body = await res.json()
      if (body && body.error) msg = body.error
    } catch (_) { /* 非 JSON */ }
    throw new Error(msg)
  }
  if (res.status === 204) return null
  return res.json()
}

export const getMe = () => request('/api/me')

/** 书架：书籍 + 章节索引 + 进度 + 每本用本会话公钥包装的 DEK。 */
export const loadLibrary = (publicKey) =>
  request('/api/web/library', {
    method: 'POST',
    body: JSON.stringify({ public_key: publicKey }),
  })

/** 批量拉章节密文（NSCH base64）。 */
export const loadChapters = (bookId, indexes, publicKey) =>
  request('/api/web/chapters', {
    method: 'POST',
    body: JSON.stringify({ book_id: bookId, indexes, public_key: publicKey }),
  })

/** 拉取增量进度。 */
export const pullProgress = (since) => {
  const q = since ? '?since=' + encodeURIComponent(since) : ''
  return request('/api/sync/progress' + q)
}

/** 批量上报进度（服务端按位置合并，返回合并后的全量）。 */
export const pushProgress = (items) =>
  request('/api/sync/progress', {
    method: 'POST',
    body: JSON.stringify({ items }),
  })

export const logout = () => request('/api/logout', { method: 'POST' })
