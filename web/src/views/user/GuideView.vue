<script setup>
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { resetGuide } from '../../user/guide'

const router = useRouter()

// FAQ 用「同一时刻只展开一条」的手风琴：手机上同时展开多条会把下面的内容推得很远。
const open = ref(0)

/** 重看引导：清标记 → 回书架 → 刷新，让 UserShell 重新走一遍首启流程。
 *  先等路由落地再 reload，否则刷新会把还没完成的跳转打断，停在当前页。 */
function replay() {
  resetGuide()
  router.push('/library').then(() => location.reload())
}

const items = [  {
    q: '怎么导入一本 TXT？',
    a: [
      '控制台 → 书籍 → 上传，选中 txt 文件即可。编码（UTF-8 / GBK）会自动嗅探，识别不出来时可以在上传面板里手动指定。',
      '切章默认匹配「第 X 章」「Chapter N」等常见写法；如果你的文件用「卷一 · ×××」这类自定义标题，可以在同一面板填一条正则，预览确认后再入库。',
    ],
  },
  {
    q: '怎么切换音色、调语速？',
    a: [
      '朗读过程中，阅读页底部会浮出播放条，点音色名即可在已配置的音色间切换；Android 也可以直接在通知栏切上一章 / 下一章。',
      '语速在「我的 → 朗读」里调，范围 0.5–2.0 倍，下次开始朗读时生效。Web 端用的是系统语音（speechSynthesis），音色取决于浏览器与操作系统，不消耗服务端 TTS。',
    ],
  },
  {
    q: '局域网连接怎么配？',
    a: [
      '手机与服务器在同一个 WiFi 下时：设置 → 服务器设置 → 局域网发现，列表里点一下就能连；或者用控制台「设备」页生成的 6 位配对码登录。',
      '换地址不会让你重新配对：设备令牌跟着设备走，地址只是入口。出门在外把地址切回公网域名即可，阅读进度不受影响。',
    ],
  },
  {
    q: '多端进度是怎么合并的？',
    a: [
      '服务端按「章号 → 章内字符偏移」比较，保留更靠后的那一个，而不是「谁最后写入算谁」。所以 A 读到第 30 章、B 随手翻回第 12 章看一眼，A 不会被拽回第 12 章。',
      '代价是「主动往前跳章」不会同步出去。真需要回退时，在目标设备上手动跳一次章，等下一次同步即可。前台每 5 分钟合并一次，切前后台会立刻触发。',
    ],
  },
]
</script>

<template>
  <div class="guide">
    <div class="card faq" style="margin-bottom:14px">
      <div
        v-for="(it, i) in items"
        :key="it.q"
        class="faq-item"
        :class="{ open: open === i }"
      >
        <button class="faq-q" type="button" @click="open = open === i ? -1 : i">
          <span class="idx">{{ String(i + 1).padStart(2, '0') }}</span>
          <span>{{ it.q }}</span>
          <span class="sign" aria-hidden="true"></span>
        </button>
        <div class="faq-a">
          <div>
            <p v-for="(p, j) in it.a" :key="j">{{ p }}</p>
          </div>
        </div>
      </div>
    </div>

    <div class="card groupcard" style="margin-bottom:14px">
      <button
        class="row"
        style="width:100%;background:none;border:0;cursor:pointer;text-align:left"
        @click="replay"
      >
        <div class="label">重看新手引导</div>
        <div class="value" style="color:var(--secondary)">›</div>
      </button>
    </div>

    <div style="margin:16px 4px 0;font-size:11.5px;color:var(--secondary);line-height:1.7">
      还有别的问题？控制台「审计」页能看到每一次同步与失败的原始记录，排障时比截图有用。
    </div>
  </div>
</template>
