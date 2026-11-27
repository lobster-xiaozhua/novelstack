<script setup>
import { onMounted, ref } from 'vue'
import { useRoute } from 'vue-router'
import { getMe, logout } from './api'

const me = ref(null)
const route = useRoute()

onMounted(async () => {
  try { me.value = await getMe() } catch (_) { /* 401 已跳转 */ }
})
</script>

<template>
  <div class="app">
    <aside class="side">
      <div class="brand">
        <div class="mark">NS</div>
        <div>
          <b>novelstack</b>
          <span>PRIVATE READER</span>
        </div>
      </div>

      <nav class="nav">
        <div class="group">运营</div>
        <router-link to="/overview"><i class="dot" />概览</router-link>
        <router-link to="/security"><i class="dot" />加密健康度</router-link>

        <div class="group">内容</div>
        <router-link to="/tts"><i class="dot" />TTS 朗读</router-link>
        <router-link to="/search"><i class="dot" />搜索与覆盖度</router-link>
        <router-link to="/telemetry"><i class="dot" />体验调优</router-link>
        <router-link to="/audit"><i class="dot" />审计</router-link>

        <div class="group">维护</div>
        <router-link to="/cache"><i class="dot" />缓存管理</router-link>
        <router-link to="/devices"><i class="dot" />设备与配对</router-link>
      </nav>

      <div class="side-foot">
        <div class="who">{{ me ? me.username : '—' }} · 单用户</div>
        <div>只读控制台 · 无解密入口</div>
        <button type="button" @click="logout">退出登录</button>
      </div>
    </aside>

    <main class="main">
      <router-view />
    </main>
  </div>
</template>
