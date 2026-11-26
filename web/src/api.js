// 所有请求都带同源 cookie；401 统一回登录页（会话过期）。
async function request(path, options = {}) {
  const res = await fetch(path, {
    credentials: 'same-origin',
    headers: options.body ? { 'Content-Type': 'application/json' } : {},
    ...options,
  })
  if (res.status === 401) {
    location.replace('/admin/login')
    throw new Error('unauthenticated')
  }
  if (!res.ok) {
    let msg = `HTTP ${res.status}`
    try {
      const body = await res.json()
      if (body && body.error) msg = body.error
    } catch (_) { /* ignore */ }
    throw new Error(msg)
  }
  if (res.status === 204) return null
  return res.json()
}

export const getDashboard = () => request('/api/admin/dashboard')

export const getAudit = (params = {}) => {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v !== '' && v !== null && v !== undefined) q.set(k, v)
  }
  const s = q.toString()
  return request('/api/admin/audit' + (s ? `?${s}` : ''))
}

export const clearCache = (body) =>
  request('/api/admin/cache/clear', { method: 'POST', body: JSON.stringify(body) })

export const pruneCache = (body) =>
  request('/api/admin/cache/prune', { method: 'POST', body: JSON.stringify(body) })

export const getMe = () => request('/api/me')

// 第五阶段：移动端设备与一次性配对码
export const getDevices = () => request('/api/admin/devices')
export const revokeDevice = (id) =>
  request(`/api/admin/devices/${encodeURIComponent(id)}`, { method: 'DELETE' })
export const createPairing = () => request('/api/admin/pairing', { method: 'POST' })

export async function logout() {
  try {
    await fetch('/api/logout', { method: 'POST', credentials: 'same-origin' })
  } finally {
    location.replace('/admin/login')
  }
}
