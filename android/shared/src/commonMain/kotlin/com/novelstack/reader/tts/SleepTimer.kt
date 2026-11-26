package com.novelstack.reader.tts

import kotlinx.datetime.Clock
import kotlinx.datetime.Instant
import kotlin.time.Duration.Companion.minutes

/**
 * 定时关闭（可注入时钟，便于单元测试验证「到点触发」）。
 *
 * 两种语义：
 *  - [SleepMode.MIN_15/MIN_30/MIN_60]：从 [start] 时刻起经过 N 分钟后到点；
 *  - [SleepMode.END_OF_CHAPTER]：不按时间到点，由上层在「当前章播完」时调用
 *    [onChapterEnded] 触发。
 *
 * 本类只做判定，不直接操作播放器或前台服务；上层观察 [expired] 后执行优雅停止。
 */
class SleepTimer(private val clock: Clock = Clock.System) {

    private var mode: SleepMode = SleepMode.OFF
    private var deadline: Instant? = null

    /** 是否已到点（计时模式）或被章节结束事件触发。 */
    var expired: Boolean = false
        private set

    /** 当前配置的模式。 */
    fun mode(): SleepMode = mode

    /** 剩余毫秒数，供 UI 倒计时展示；OFF / 章节模式 / 已到点返回 0。 */
    fun remainingMs(now: Instant = clock.now()): Long {
        if (mode == SleepMode.OFF || mode == SleepMode.END_OF_CHAPTER || expired) return 0L
        val d = deadline ?: return 0L
        return (d - now).inWholeMilliseconds.coerceAtLeast(0)
    }

    /** 启动/切换定时模式；重复调用以最后一次为准。 */
    fun start(newMode: SleepMode, now: Instant = clock.now()) {
        mode = newMode
        expired = false
        deadline = if (newMode.minutes > 0) now.plus(newMode.minutes.minutes) else null
    }

    /** 取消定时。 */
    fun cancel() {
        mode = SleepMode.OFF
        deadline = null
        expired = false
    }

    /**
     * 计时模式下由上层轮询调用：到点则置 expired=true 并返回 true（仅首次）。
     * END_OF_CHAPTER 模式不在此判定。
     */
    fun tick(now: Instant = clock.now()): Boolean {
        if (expired || mode == SleepMode.OFF || mode == SleepMode.END_OF_CHAPTER) return false
        val d = deadline ?: return false
        if (now >= d) {
            expired = true
            return true
        }
        return false
    }

    /**
     * 当前章播放完毕时调用：仅 END_OF_CHAPTER 模式下触发到点。
     */
    fun onChapterEnded(): Boolean {
        if (expired || mode != SleepMode.END_OF_CHAPTER) return false
        expired = true
        return true
    }
}
