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

/** 管理端：列出当前用户的书籍（GET /api/books，session 鉴权）。 */
export const getBooks = () => request('/api/books')

/**
 * 管理端：上传书籍（POST /api/books，multipart/form-data）。
 *
 * 用 XMLHttpRequest 而不是 fetch：fetch 没有上传进度事件，XHR 的
 * upload.onprogress 才能驱动平滑进度条。字段顺序刻意把 title 等元数据
 * 放在 file 之前——服务端用 MultipartReader 流式消费，file part 必须
 * 在遍历到的当下读取，且要求 title 已先出现，否则直接 400。
 *
 * 服务端继续走原有的流式信封加密（io.TeeReader + 分块 encryptCounting），
 * 明文不落盘、内存不累积；这里只负责把 File 原样送上去，不读取文件内容。
 */
export function uploadBook({ file, title, author, tags, description, onProgress }) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    xhr.open('POST', '/api/books')
    xhr.withCredentials = true // 同源会话 cookie
    if (xhr.upload) {
      xhr.upload.onprogress = (e) => {
        if (e.lengthComputable && onProgress) onProgress(e.loaded / e.total)
      }
    }
    xhr.onload = () => {
      let body = null
      try { body = JSON.parse(xhr.responseText) } catch (_) { /* 非 JSON */ }
      if (xhr.status === 401) {
        location.replace('/admin/login')
        reject(new Error('unauthenticated'))
        return
      }
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(body)
        return
      }
      const msg = body && body.error ? body.error : `HTTP ${xhr.status}`
      reject(new Error(msg))
    }
    xhr.onerror = () => reject(new Error('网络异常，上传已中断'))
    xhr.onabort = () => reject(new Error('上传已取消'))

    const form = new FormData()
    form.append('title', title || file.name.replace(/\.txt$/i, ''))
    form.append('author', author || '')
    form.append('tags', tags || '')
    form.append('description', description || '')
    form.append('file', file, file.name)
    xhr.send(form)
  })
}

// 第九阶段：全链路遥测（只读聚合与明细）
const telemetryQuery = (days) => {
  const d = Number(days) || 7
  return `days=${encodeURIComponent(d)}`
}

export const getTelemetryOverview = (days = 7) =>
  request(`/api/admin/telemetry/overview?${telemetryQuery(days)}`)

export const getTelemetryHeatmap = (days = 7) =>
  request(`/api/admin/telemetry/heatmap?${telemetryQuery(days)}`)

export const getTelemetrySlowChapters = (days = 7) =>
  request(`/api/admin/telemetry/slow-chapters?${telemetryQuery(days)}`)

export const getTelemetryCrashes = (days = 7, limit = 50) =>
  request(`/api/admin/telemetry/crashes?${telemetryQuery(days)}&limit=${encodeURIComponent(limit)}`)

export const getTelemetrySeries = (days = 7, kind = '') =>
  request(`/api/admin/telemetry/series?${telemetryQuery(days)}${kind ? `&kind=${encodeURIComponent(kind)}` : ''}`)

// 第五阶段：移动端设备与一次性配对码
export const getDevices = () => request('/api/admin/devices')
export const revokeDevice = (id) =>
  request(`/api/admin/devices/${encodeURIComponent(id)}`, { method: 'DELETE' })
export const createPairing = () => request('/api/admin/pairing', { method: 'POST' })

// 第十阶段：局域网 6 位短码配对（含可扫码的 endpoint）
export const createLanPairing = () => request('/api/admin/lan/pairing', { method: 'POST' })
export const getLanInfo = () => request('/api/lan/info')

// 第九阶段：阅读统计与数据导出
export const getReadingStats = () => request('/api/stats/reading')
export const getExportPreview = () => request('/api/admin/export/preview')

// 终端防御阶段：IP 黑名单查看与解封。
export const getBlocklist = () => request('/api/admin/blocklist')
export const unblockIP = (ip) =>
  request('/api/admin/blocklist/' + encodeURIComponent(ip), { method: 'DELETE' })

/**
 * 下载导出包。浏览器无法用 fetch+blob 直接触发下载时需要自建 <a>：
 * 这里先取回 blob，再由临时链接落盘，文件名优先用服务端 Content-Disposition。
 */
export async function downloadExport({ password, bookIds = [], format = 'txt' }) {
  const res = await fetch('/api/admin/export', {
    method: 'POST',
    credentials: 'same-origin',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password, book_ids: bookIds, format }),
  })
  if (!res.ok) {
    if (res.status === 401) throw new Error('密码错误或会话已过期')
    let msg = `HTTP ${res.status}`
    try {
      const body = await res.json()
      if (body && body.error) msg = body.error
    } catch (_) { /* ignore */ }
    throw new Error(msg)
  }
  const disposition = res.headers.get('Content-Disposition') || ''
  const m = /filename="?([^"]+)"?/.exec(disposition)
  const name = m ? m[1] : `novelstack-export.${format === 'epub' ? 'zip' : 'zip'}`
  const blob = await res.blob()
  const url = URL.createObjectURL(blob)
  const a = document.createElement('a')
  a.href = url
  a.download = name
  document.body.appendChild(a)
  a.click()
  a.remove()
  setTimeout(() => URL.revokeObjectURL(url), 10000)
  return blob.size
}

export async function logout() {
  try {
    await fetch('/api/logout', { method: 'POST', credentials: 'same-origin' })
  } finally {
    location.replace('/admin/login')
  }
}
