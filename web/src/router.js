import { createRouter, createWebHistory } from 'vue-router'

import Overview from './views/Overview.vue'
import Security from './views/Security.vue'
import TTS from './views/TTS.vue'
import Search from './views/Search.vue'
import Audit from './views/Audit.vue'
import Cache from './views/Cache.vue'
import Devices from './views/Devices.vue'
import Telemetry from './views/Telemetry.vue'

const routes = [
  { path: '/', redirect: '/overview' },
  { path: '/overview', name: 'overview', component: Overview, meta: { title: '概览' } },
  { path: '/security', name: 'security', component: Security, meta: { title: '加密健康度' } },
  { path: '/tts', name: 'tts', component: TTS, meta: { title: 'TTS 朗读' } },
  { path: '/search', name: 'search', component: Search, meta: { title: '搜索与覆盖度' } },
  { path: '/audit', name: 'audit', component: Audit, meta: { title: '审计' } },
  { path: '/cache', name: 'cache', component: Cache, meta: { title: '缓存管理' } },
  { path: '/devices', name: 'devices', component: Devices, meta: { title: '设备与配对' } },
  { path: '/telemetry', name: 'telemetry', component: Telemetry, meta: { title: '体验调优' } },
  { path: '/:pathMatch(.*)*', redirect: '/overview' },
]

export default createRouter({
  history: createWebHistory('/admin'),
  routes,
})
