// Service Worker：只负责「断网也能打开阅读界面」。
//
// 章节内容不经过 SW 缓存（接口是 POST，且密文已存 IndexedDB），
// 这里只缓存 app shell 与静态资源，避免把用户数据写进 CacheStorage。

const VERSION = 'ns-v1'
const SHELL = ['/', '/login']

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(VERSION)
      // 未登录时 / 会 303 到 /login，add 会因重定向失败；逐个 catch 避免安装中断。
      .then((cache) => Promise.all(SHELL.map((u) => cache.add(u).catch(() => {}))))
      .then(() => self.skipWaiting()),
  )
})

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== VERSION).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  )
})

self.addEventListener('fetch', (event) => {
  const req = event.request
  if (req.method !== 'GET') return
  const url = new URL(req.url)
  if (url.origin !== self.location.origin) return
  // 接口永远走网络：陈旧数据比没有数据更危险（进度、权限）。
  if (url.pathname.startsWith('/api/')) return

  if (req.mode === 'navigate') {
    event.respondWith(
      fetch(req)
        .then((res) => {
          const copy = res.clone()
          caches.open(VERSION).then((c) => c.put(req, copy)).catch(() => {})
          return res
        })
        .catch(async () => {
          const cached = await caches.match(req)
          if (cached) return cached
          const shell = await caches.match('/')
          return shell || new Response('离线', { status: 503, headers: { 'Content-Type': 'text/plain; charset=utf-8' } })
        }),
    )
    return
  }

  // 静态资源：缓存优先，后台更新。
  if (url.pathname.startsWith('/assets/') || url.pathname === '/sw.js') {
    event.respondWith(
      caches.match(req).then((cached) => {
        const network = fetch(req)
          .then((res) => {
            const copy = res.clone()
            caches.open(VERSION).then((c) => c.put(req, copy)).catch(() => {})
            return res
          })
          .catch(() => cached)
        return cached || network
      }),
    )
  }
})
