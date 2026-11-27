<script setup>
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { createLanPairing, createPairing, getDevices, getLanInfo, revokeDevice } from '../api'
import { ago, ts } from '../format'
import { lanPairingPayload, qrSvg } from '../vendor/qr'

const data = ref(null)
const error = ref('')
const loading = ref(false)
const pairing = ref(null)
const busy = ref(false)
const copied = ref(false)
const confirmRevoke = ref(null)
let timer = null

// 第十阶段：局域网 6 位短码配对
const lanPairing = ref(null)
const lanInfo = ref(null)
const lanRemain = ref(0)
const lanSvg = ref('')

const devices = computed(() => data.value?.devices || [])
const remain = ref(0)

async function load() {
  loading.value = true
  error.value = ''
  try {
    data.value = await getDevices()
  } catch (e) {
    error.value = e.message || String(e)
  } finally {
    loading.value = false
  }
}

async function makePairing() {
  busy.value = true
  error.value = ''
  try {
    pairing.value = await createPairing()
    copied.value = false
    remain.value = pairing.value.expires_in_s
  } catch (e) {
    error.value = e.message || String(e)
  } finally {
    busy.value = false
  }
}

async function copyCode() {
  if (!pairing.value) return
  try {
    await navigator.clipboard.writeText(pairing.value.code)
    copied.value = true
  } catch (_) {
    copied.value = false
  }
}

async function makeLanPairing() {
  busy.value = true
  error.value = ''
  try {
    if (!lanInfo.value) lanInfo.value = await getLanInfo()
    const p = await createLanPairing()
    lanPairing.value = p
    lanRemain.value = p.expires_in_s
    const endpoint = p.endpoint || lanInfo.value?.endpoint || location.origin
    lanSvg.value = qrSvg(lanPairingPayload(p.code, endpoint), 5)
  } catch (e) {
    error.value = e.message || String(e)
  } finally {
    busy.value = false
  }
}

function copyLanPayload() {
  if (!lanPairing.value) return
  const endpoint = lanPairing.value.endpoint || lanInfo.value?.endpoint || location.origin
  navigator.clipboard.writeText(lanPairingPayload(lanPairing.value.code, endpoint)).catch(() => {})
}

async function doRevoke() {
  if (!confirmRevoke.value) return
  busy.value = true
  try {
    await revokeDevice(confirmRevoke.value.id)
    confirmRevoke.value = null
    await load()
  } catch (e) {
    error.value = e.message || String(e)
  } finally {
    busy.value = false
  }
}

function statusOf(d) {
  if (d.revoked_at) return { label: '已撤销', cls: 'bad' }
  if (d.expires_at && new Date(d.expires_at).getTime() < Date.now()) return { label: '已过期', cls: 'warn' }
  return { label: '有效', cls: 'ok' }
}

onMounted(() => {
  load()
  getLanInfo().then((i) => { lanInfo.value = i }).catch(() => {})
  timer = setInterval(() => {
    if (remain.value > 0) remain.value -= 1
    if (lanRemain.value > 0) lanRemain.value -= 1
  }, 1000)
})
onUnmounted(() => clearInterval(timer))
</script>

<template>
  <section>
    <header class="top">
      <div>
        <h1>设备与配对</h1>
        <p class="sub">
          device token 与浏览器会话是两套凭证：手机只能读书籍密文与同步元数据，
          不能访问控制台；撤销一台设备不影响其它设备与浏览器登录。
        </p>
      </div>
      <div class="actions">
        <button class="btn ghost" :disabled="loading" @click="load">刷新</button>
        <button class="btn ghost" :disabled="busy" @click="makeLanPairing">生成局域网码</button>
        <button class="btn primary" :disabled="busy" @click="makePairing">生成配对码</button>
      </div>
    </header>

    <div v-if="error" class="error">{{ error }}</div>

    <div v-if="pairing" class="card pairing">
      <div>
        <div class="label">一次性配对码（10 分钟有效，只能使用一次）</div>
        <div class="code">{{ pairing.code }}</div>
        <div class="hint">
          在 App 登录页选择「一次性配对码」输入；剩余
          <b>{{ Math.floor(remain / 60) }}:{{ String(remain % 60).padStart(2, '0') }}</b>
        </div>
      </div>
      <div class="pair-actions">
        <button class="btn ghost" @click="copyCode">{{ copied ? '已复制' : '复制' }}</button>
        <button class="btn ghost" @click="pairing = null">关闭</button>
      </div>
    </div>

    <div v-if="lanPairing" class="card pairing lan-pair">
      <div class="qr" v-html="lanSvg"></div>
      <div class="lan-info">
        <div class="label">局域网配对码（6 位，{{ Math.floor(lanRemain / 60) }}:{{ String(lanRemain % 60).padStart(2, '0') }}，绑定来源 IP）</div>
        <div class="code">{{ lanPairing.code }}</div>
        <div class="hint">
          App「服务器设置 → 扫配对二维码」扫描上方二维码即可；
          或在局域网发现里选本机后手动输入这 6 位数字。
        </div>
        <div class="hint mono" v-if="lanPairing.endpoint">{{ lanPairing.endpoint }}</div>
        <div class="pair-actions" style="margin-top: 10px">
          <button class="btn ghost" @click="copyLanPayload">复制扫码内容</button>
          <button class="btn ghost" @click="lanPairing = null">关闭</button>
        </div>
      </div>
    </div>

    <section class="grid cols-3">
      <div class="card kpi">
        <div class="label">有效设备</div>
        <div class="value">{{ data?.active ?? '—' }}</div>
      </div>
      <div class="card kpi">
        <div class="label">已撤销</div>
        <div class="value">{{ data?.revoked ?? '—' }}</div>
      </div>
      <div class="card kpi">
        <div class="label">登记总数</div>
        <div class="value">{{ devices.length }}</div>
      </div>
    </section>

    <div class="card">
      <table class="table">
        <thead>
          <tr>
            <th>设备</th>
            <th>ID</th>
            <th>公钥</th>
            <th>创建</th>
            <th>最近使用</th>
            <th>状态</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr v-for="d in devices" :key="d.id">
            <td>
              <b>{{ d.name }}</b>
              <div class="mono dim">{{ d.token_prefix }}…</div>
            </td>
            <td class="mono">{{ d.id }}</td>
            <td>
              <span :class="['pill', d.has_public_key ? 'ok' : 'warn']">
                {{ d.has_public_key ? '已登记' : '无（仅元数据）' }}
              </span>
            </td>
            <td class="dim">{{ ts(d.created_at) }}</td>
            <td class="dim">{{ d.last_used_at ? ago(d.last_used_at) : '从未' }}</td>
            <td><span :class="['pill', statusOf(d).cls]">{{ statusOf(d).label }}</span></td>
            <td class="right">
              <button
                v-if="!d.revoked_at"
                class="btn ghost danger"
                :disabled="busy"
                @click="confirmRevoke = d"
              >
                撤销
              </button>
            </td>
          </tr>
          <tr v-if="!devices.length">
            <td colspan="7" class="empty">还没有设备。点右上角「生成配对码」开始。</td>
          </tr>
        </tbody>
      </table>
    </div>

    <div class="card note">
      <div class="label">安全边界</div>
      <ul>
        <li>服务端只保存 token 的 SHA-256；泄露数据库也无法反推出可用凭证。</li>
        <li>设备公钥用于包装书籍/音频 DEK；私钥只在手机 Keystore 内，服务端从不接触。</li>
        <li>撤销后该 token 立即返回 401；已缓存的离线章节仍可读（本地密钥未被远程删除）。</li>
        <li>如需彻底失效某台手机：撤销设备 → 在手机上「退出并清除本地数据」。</li>
      </ul>
    </div>

    <div v-if="confirmRevoke" class="overlay" @click.self="confirmRevoke = null">
      <div class="modal">
        <h3>撤销「{{ confirmRevoke.name }}」？</h3>
        <p class="dim">
          撤销后该设备无法再同步或预取新章节，已缓存的离线内容不受影响。
          设备 ID：<span class="mono">{{ confirmRevoke.id }}</span>
        </p>
        <div class="modal-actions">
          <button class="btn ghost" @click="confirmRevoke = null">取消</button>
          <button class="btn danger" :disabled="busy" @click="doRevoke">确认撤销</button>
        </div>
      </div>
    </div>
  </section>
</template>

<style scoped>
.pairing {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
  border-color: rgba(143, 180, 217, 0.45);
}
.code {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 28px;
  letter-spacing: 2px;
  margin: 8px 0;
  color: var(--accent, #8fb4d9);
}
.hint {
  font-size: 12px;
  color: var(--fg-dim, #8b97a6);
}
.pair-actions {
  display: flex;
  gap: 8px;
}
.lan-pair {
  align-items: flex-start;
  border-color: rgba(120, 200, 160, 0.45);
}
.qr {
  background: #fff;
  padding: 8px;
  border-radius: 8px;
  line-height: 0;
  flex: 0 0 auto;
}
.qr svg {
  display: block;
}
.lan-info {
  flex: 1;
}
.note ul {
  margin: 8px 0 0;
  padding-left: 18px;
  color: var(--fg-dim, #8b97a6);
  font-size: 13px;
  line-height: 1.8;
}
.empty {
  text-align: center;
  color: var(--fg-dim, #8b97a6);
  padding: 28px 0;
}
.right {
  text-align: right;
}
.mono {
  font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
  font-size: 12px;
}
.dim {
  color: var(--fg-dim, #8b97a6);
}
</style>
