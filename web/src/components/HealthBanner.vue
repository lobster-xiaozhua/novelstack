<script setup>
// 只读健康横幅：数据来自 /api/admin/dashboard 的 health 字段。
// 这里不做任何解密，也不展示审计 detail（避免正文/查询词经摘要泄漏）。
const props = defineProps({
  health: { type: Object, default: null },
})

const title = {
  bad: '系统健康告警',
  warn: '系统健康提醒',
  ok: '系统健康',
}
</script>

<template>
  <div v-if="health && health.level" class="hb" :class="'hb-' + health.level" role="status">
    <div class="hb-head">
      <span class="hb-dot"></span>
      <strong>{{ title[health.level] || '系统健康' }}</strong>
      <span v-if="health.level === 'bad'" class="hb-count">{{ health.alerts.length }} 项需要处理</span>
      <span v-else-if="health.level === 'warn'" class="hb-count">不影响运行</span>
      <span v-else class="hb-count">正常</span>
    </div>
    <ul v-if="health.alerts.length" class="hb-list">
      <li v-for="a in health.alerts" :key="a.type">{{ a.summary }}</li>
    </ul>
  </div>
</template>

<style scoped>
.hb {
  border: 1px solid var(--line);
  border-radius: var(--radius);
  padding: 12px 14px;
  margin: 0 0 14px;
  background: var(--panel);
}
.hb-bad {
  border-color: rgba(226, 109, 117, .55);
  background: rgba(226, 109, 117, .09);
  color: #f2b9be;
}
.hb-warn {
  border-color: rgba(224, 164, 88, .45);
  background: rgba(224, 164, 88, .06);
  color: #e5c99a;
}
.hb-ok {
  border-color: rgba(99, 192, 140, .25);
  color: #a9e2c4;
}
.hb-head { display: flex; align-items: center; gap: 9px; }
.hb-head strong { font-weight: 650; }
.hb-count { margin-left: auto; font-size: 12px; opacity: .85; }
.hb-dot { width: 8px; height: 8px; border-radius: 50%; background: currentColor; box-shadow: 0 0 0 3px rgba(255,255,255,.05); }
.hb-list { margin: 8px 0 0; padding-left: 18px; }
.hb-list li { margin: 3px 0; line-height: 1.5; }
</style>
