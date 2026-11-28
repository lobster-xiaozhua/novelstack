<script setup>
import { onUnmounted, watch } from 'vue'

/**
 * iOS 风格的底部确认菜单（Action Sheet）。
 *
 * 用「悬浮卡片 + 独立取消卡」的形态，而不是贴着底边切一刀：
 * 顶角圆、底角直角那种做法只能写 `border-radius: 16px 16px 0 0`，
 * 一旦要 G3 连续曲率就得给四条边分别配遮罩，成本远高于收益。
 * 悬浮卡片四角都能走同一套九宫格蒙版，形状和全站一致。
 */
const props = defineProps({
  open: { type: Boolean, default: false },
  title: { type: String, default: '' },
  message: { type: String, default: '' },
  items: { type: Array, default: () => [] },
  cancelText: { type: String, default: '取消' },
})
const emit = defineEmits(['update:open', 'select'])

function close() {
  emit('update:open', false)
}

function pick(item) {
  if (item.disabled) return
  emit('select', item.value)
  close()
}

function onKey(e) {
  if (e.key === 'Escape') close()
}

watch(
  () => props.open,
  (v) => {
    if (v) window.addEventListener('keydown', onKey)
    else window.removeEventListener('keydown', onKey)
  },
)
onUnmounted(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="sheet-root" role="dialog" aria-modal="true">
      <div class="sheet-backdrop" @click="close"></div>
      <div class="sheet-panel">
        <div class="sheet-card">
          <div v-if="title || message" class="sheet-head">
            <b v-if="title">{{ title }}</b>
            <span v-if="message">{{ message }}</span>
          </div>
          <button
            v-for="it in items"
            :key="it.value"
            class="sheet-item"
            :class="{ danger: it.danger, strong: it.strong }"
            type="button"
            :disabled="it.disabled"
            @click="pick(it)"
          >
            {{ it.label }}
          </button>
        </div>
        <div class="sheet-card">
          <button class="sheet-item strong" type="button" @click="close">{{ cancelText }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
