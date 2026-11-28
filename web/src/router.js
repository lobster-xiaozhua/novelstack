import { createRouter, createWebHistory } from 'vue-router'

// 用户端与管理端共用一个构建产物，路由前缀区分；所有页面懒加载分包，
// 首屏只加载外壳 + 当前页（Vite 自动产出独立 chunk）。
const routes = [
  {
    path: '/',
    component: () => import('./views/user/UserShell.vue'),
    children: [
      { path: '', redirect: '/library' },
      { path: 'library', name: 'user-shelf', component: () => import('./views/user/ShelfView.vue') },
      { path: 'discover', name: 'user-discover', component: () => import('./views/user/DiscoverView.vue') },
      { path: 'history', name: 'user-history', component: () => import('./views/user/HistoryView.vue') },
      { path: 'settings', name: 'user-settings', component: () => import('./views/user/UserSettingsView.vue') },
    ],
  },
  {
    path: '/read/:id',
    name: 'reader',
    component: () => import('./views/user/ReaderView.vue'),
    meta: { title: '阅读' },
  },
  {
    path: '/admin',
    component: () => import('./views/AdminShell.vue'),
    children: [
      { path: '', redirect: '/admin/overview' },
      { path: 'overview', name: 'admin-overview', component: () => import('./views/Overview.vue'), meta: { title: '概览' } },
      { path: 'security', name: 'admin-security', component: () => import('./views/Security.vue'), meta: { title: '加密健康度' } },
      { path: 'tts', name: 'admin-tts', component: () => import('./views/TTS.vue'), meta: { title: 'TTS 朗读' } },
      { path: 'search', name: 'admin-search', component: () => import('./views/Search.vue'), meta: { title: '搜索覆盖度' } },
      { path: 'cache', name: 'admin-cache', component: () => import('./views/Cache.vue'), meta: { title: '缓存管理' } },
      { path: 'devices', name: 'admin-devices', component: () => import('./views/Devices.vue'), meta: { title: '设备与配对' } },
      { path: 'blocklist', name: 'admin-blocklist', component: () => import('./views/Blocklist.vue'), meta: { title: 'IP 防护' } },
      { path: 'audit', name: 'admin-audit', component: () => import('./views/Audit.vue'), meta: { title: '审计' } },
      { path: 'stats', name: 'admin-stats', component: () => import('./views/Stats.vue'), meta: { title: '阅读统计' } },
      { path: 'telemetry', name: 'admin-telemetry', component: () => import('./views/Telemetry.vue'), meta: { title: '体验调优' } },
    ],
  },
  { path: '/:pathMatch(.*)*', redirect: '/library' },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
  scrollBehavior(to, from, saved) {
    return saved || { top: 0 }
  },
})

router.afterEach((to) => {
  const base = 'novelstack'
  document.title = to.meta?.title ? `${to.meta.title} · ${base}` : base
})

export default router
