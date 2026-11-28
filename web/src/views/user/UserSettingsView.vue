<script setup>
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { state, refreshProgress, flushProgress, clearLocal } from '../../user/store'
import { logout, getMe } from '../../userApi'
import { hasSessionKeys } from '../../crypto/keys'
import * as idb from '../../offline/idb'

const router = useRouter()
const me = ref(null)
const cached = ref({ books: 0, chapters: 0 })
const busy = ref(false)
const msg = ref('')

const syncText = computed(() => {
  if (!state.lastSync) return '尚未同步'
  const sec = Math.floor((Date.now() - state.lastSync) / 1000)
  if (sec < 60) return `${sec} 秒前`
  if (sec < 3600) return `${Math.floor(sec / 60)} 分钟前`
  return `${Math.floor(sec / 3600)} 小时前`
})

async function countCache() {
  const metas = await idb.listBookMeta().catch(() => [])
  let chapters = 0
  for (const m of metas) {
    chapters += (await idb.listCachedIndexes(m.id).catch(() => [])).length
  }
  cached.value = { books: metas.length, chapters }
}

async function syncNow() {
  busy.value = true
  msg.value = ''
  try {
    await flushProgress()
    await refreshProgress()
    msg.value = '已与服务器对齐'
    await countCache()
  } catch (e) {
    msg.value = e.message || String(e)
  } finally {
    busy.value = false
  }
}

async function wipeCache() {
  await idb.clearAll().catch(() => {})
  await countCache()
  msg.value = '本地密文缓存已清空'
}

async function signOut() {
  try { await logout() } catch (_) { /* 会话已过期也继续 */ }
  await clearLocal()
  sessionStorage.removeItem('novelstack.web.kp')
  location.replace('/login')
}

onMounted(async () => {
  try { me.value = await getMe() } catch (_) { /* 401 已跳转 */ }
  await countCache()
})
</script>

<template>
  <div>
    <div class="card groupcard" style="margin-bottom:14px">
      <div class="row">
        <div class="label">账号</div>
        <div class="value">{{ me?.username || '—' }}</div>
      </div>
      <div class="row">
        <div class="label">同步</div>
        <div class="value">
          {{ syncText }}
          <span v-if="state.offline" class="pill warn" style="margin-left:6px">离线</span>
        </div>
      </div>
      <div class="row">
        <div class="label">会话密钥</div>
        <div class="value">{{ hasSessionKeys() ? '仅本标签页' : '已失效' }}</div>
      </div>
      <div class="row">
        <div class="label">离线缓存</div>
        <div class="value">{{ cached.books }} 本 · {{ cached.chapters }} 章</div>
      </div>
    </div>

    <div class="card groupcard" style="margin-bottom:14px">
      <button class="row" style="width:100%;background:none;border:0;cursor:pointer;text-align:left" :disabled="busy" @click="syncNow">
        <div class="label">立即同步</div>
        <div class="value" style="color:var(--blue)">{{ busy ? '同步中…' : '›' }}</div>
      </button>
      <button class="row" style="width:100%;background:none;border:0;cursor:pointer;text-align:left" @click="wipeCache">
        <div class="label">清空离线缓存</div>
        <div class="value" style="color:var(--secondary)">›</div>
      </button>
    </div>

    <div v-if="msg" class="note">{{ msg }}</div>

    <div class="card groupcard">
      <button class="row" style="width:100%;background:none;border:0;cursor:pointer;text-align:left" @click="signOut">
        <div class="label" style="color:var(--red)">退出登录</div>
        <div class="value" style="color:var(--secondary)">›</div>
      </button>
    </div>

    <div style="margin:18px 4px 0;font-size:11.5px;color:var(--secondary);line-height:1.7">
      正文以 NSCH 密文缓存，密钥只在本标签页内存中；关闭标签页后离线缓存不可解密。
      关闭标签页即视为退出阅读会话。
    </div>
  </div>
</template>
