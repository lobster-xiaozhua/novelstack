<script setup>
/**
 * 空状态：细线条插画 + 一句话说明 + 一个可操作的出口。
 * 空状态最容易犯的错是「只告诉用户这里没有东西」，所以 cta 是必需的——
 * 书架空了就把他带到发现页，而不是让用户自己找下一步。
 */
defineProps({
  title: { type: String, required: true },
  desc: { type: String, default: '' },
  cta: { type: String, default: '' },
  art: { type: String, default: 'shelf' },
})
defineEmits(['action'])
</script>

<template>
  <div class="empty-state">
    <svg v-if="art === 'shelf'" class="art" viewBox="0 0 160 124" fill="none" stroke="currentColor"
         stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
      <!-- 空书架：一个柜体 + 中隔板；上层三本直立一本斜靠，下层两本，右侧留空 -->
      <rect x="28" y="20" width="104" height="88" />
      <path d="M28 76 H132" />
      <rect x="40" y="36" width="11" height="40" />
      <rect x="55" y="30" width="11" height="46" />
      <rect x="70" y="42" width="11" height="34" />
      <path d="M85 76 L93 36 L104 40 L96 76 Z" />
      <rect x="40" y="82" width="11" height="26" />
      <rect x="55" y="88" width="11" height="20" />
      <path d="M112 90 H126" opacity=".4" />
    </svg>

    <svg v-else-if="art === 'clock'" class="art" viewBox="0 0 160 124" fill="none" stroke="currentColor"
         stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
      <circle cx="80" cy="62" r="34" />
      <path d="M80 44 V63 L94 72" />
      <path d="M80 16 V24" opacity=".5" />
      <path d="M124 62 H132" opacity=".5" />
      <path d="M36 62 H28" opacity=".5" />
    </svg>

    <svg v-else class="art" viewBox="0 0 160 124" fill="none" stroke="currentColor"
         stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
      <rect x="30" y="26" width="100" height="72" rx="8" />
      <path d="M30 46 H130" />
      <path d="M50 62 H90" opacity=".55" />
      <path d="M50 74 H110" opacity=".35" />
    </svg>

    <h3>{{ title }}</h3>
    <p v-if="desc">{{ desc }}</p>
    <button v-if="cta" class="btn cta" type="button" @click="$emit('action')">{{ cta }}</button>
  </div>
</template>
