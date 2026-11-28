<script setup>
import { onMounted, ref } from 'vue'
import { getMe, logout } from '../api'

const me = ref(null)

onMounted(async () => {
  try { me.value = await getMe() } catch (_) { /* 401 已跳转登录页 */ }
})

async function signOut() {
  try { await logout() } catch (_) { /* 会话已过期 */ }
  location.replace('/admin/login')
}
</script>

<template>
  <div class="app">
    <aside class="side">
      <div class="brand">
        <div class="mark">NS</div>
        <div>
          <b>novelstack</b>
          <span>ADMIN CONSOLE</span>
        </div>
      </div>

      <nav class="nav">
        <div class="group">仪表盘</div>
        <router-link to="/admin/overview"><i class="dot" />概览</router-link>

        <div class="group">书库</div>
        <router-link to="/admin/books"><i class="dot" />书籍管理</router-link>

        <div class="group">配置</div>
        <router-link to="/admin/security"><i class="dot" />加密健康度</router-link>
        <router-link to="/admin/tts"><i class="dot" />TTS 朗读</router-link>
        <router-link to="/admin/search"><i class="dot" />搜索覆盖度</router-link>
        <router-link to="/admin/cache"><i class="dot" />缓存管理</router-link>
        <router-link to="/admin/devices"><i class="dot" />设备与配对</router-link>

        <div class="group">防护</div>
        <router-link to="/admin/blocklist"><i class="dot" />IP 防护</router-link>
        <router-link to="/admin/audit"><i class="dot" />审计</router-link>

        <div class="group">数据</div>
        <router-link to="/admin/stats"><i class="dot" />阅读统计</router-link>
        <router-link to="/admin/telemetry"><i class="dot" />体验调优</router-link>
      </nav>

      <div class="side-foot">
        <div class="who">{{ me ? me.username : '—' }} · 单用户</div>
        <div>只读控制台 · 无解密入口</div>
        <button type="button" @click="signOut">退出登录</button>
      </div>
    </aside>

    <main class="main">
      <router-view />
    </main>
  </div>
</template>
