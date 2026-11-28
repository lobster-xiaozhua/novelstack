import { createApp } from 'vue'
import router from './router'
import App from './App.vue'
import './styles.css'

createApp(App).use(router).mount('#app')

// Service Worker：只为「断网也能打开阅读界面」缓存 app shell；
// 章节密文走 IndexedDB，不进 CacheStorage。
if ('serviceWorker' in navigator) {
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {})
  })
}
