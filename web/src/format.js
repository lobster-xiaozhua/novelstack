export function bytes(n) {
  if (n === null || n === undefined) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  let i = 0
  let v = Number(n)
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v >= 100 || i === 0 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`
}

export function pct(n, digits = 1) {
  if (n === null || n === undefined || Number.isNaN(n)) return '—'
  return `${(Number(n) * 100).toFixed(digits)}%`
}

export function ms(n) {
  if (n === null || n === undefined) return '—'
  return n >= 1000 ? `${(n / 1000).toFixed(2)} s` : `${Number(n).toFixed(0)} ms`
}

export function ts(s) {
  if (!s) return '—'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  const p = (x) => String(x).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

export function ago(s) {
  if (!s) return '从未'
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return s
  const sec = Math.max(0, (Date.now() - d.getTime()) / 1000)
  if (sec < 60) return `${Math.floor(sec)} 秒前`
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`
  if (sec < 86400) return `${Math.floor(sec / 3600)} 小时前`
  return `${Math.floor(sec / 86400)} 天前`
}

export function shortHash(s, n = 12) {
  if (!s) return '—'
  return s.length > n ? s.slice(0, n) : s
}
