import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'
import { fileURLToPath, URL } from 'node:url'

export default defineConfig({
  // 用户端与管理端共用同一构建产物，均挂在站点根下（/admin/* 是前端路由）。
  base: '/',
  plugins: [vue()],
  build: {
    outDir: fileURLToPath(new URL('../internal/adminui/dist', import.meta.url)),
    emptyOutDir: true,
    assetsDir: 'assets',
    target: 'es2020',
    rollupOptions: {
      output: {
        // ECharts 由 ReadingChart 动态 import，让 Rollup 自然拆出按需 chunk；
        // 这里只把框架单独分组，避免业务 chunk 频繁变化导致缓存失效。
        manualChunks(id) {
          if (id.includes('node_modules/vue')) return 'vue'
          return undefined
        },
      },
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/login': 'http://127.0.0.1:8080',
    },
  },
})
