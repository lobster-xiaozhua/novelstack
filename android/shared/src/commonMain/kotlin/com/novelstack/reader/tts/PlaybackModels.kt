package com.novelstack.reader.tts

/**
 * 第七阶段播放控制层的纯逻辑模型。
 *
 * 这些类型不依赖任何 Android / Ktor API，放在 commonMain 以便用单元测试
 * 覆盖状态流转与定时逻辑；它们只承载「元数据 + 状态」，绝不承载音频字节。
 */

/** 定时关闭模式。END_OF_CHAPTER 表示「播完当前章」。 */
enum class SleepMode(val minutes: Int) {
    OFF(0),
    MIN_15(15),
    MIN_30(30),
    MIN_60(60),
    END_OF_CHAPTER(-1),
    ;

    companion object {
        fun fromMinutes(minutes: Int): SleepMode = when (minutes) {
            15 -> MIN_15
            30 -> MIN_30
            60 -> MIN_60
            else -> OFF
        }
    }
}

/** 播放器状态机的稳定状态。 */
enum class PlaybackStatus {
    IDLE,
    PREPARING,
    PLAYING,
    PAUSED,
    STOPPED,
    ERROR,
}

/** 通知栏/锁屏展示用的元数据：只有书名与章节标题，绝不含正文片段。 */
data class PlaybackMetadata(
    val bookTitle: String = "",
    val chapterTitle: String = "",
)

/** 控制器 → 前台服务的生命周期信号（服务据此退出前台/清理通知）。 */
sealed interface PlaybackSignal {
    /** 播放自然结束（播完 / 定时到点 / 停止），服务应停止前台并结束自身。 */
    data object FinishForeground : PlaybackSignal
}

/** 驱动播放状态机的事件。携带的均为元数据，不含音频。 */
sealed interface PlaybackEvent {
    data class Play(val bookId: String, val chapterIndex: Int) : PlaybackEvent
    data object Prepared : PlaybackEvent
    data object Started : PlaybackEvent
    data object Pause : PlaybackEvent
    data object Resume : PlaybackEvent
    data object Stop : PlaybackEvent
    data class SegmentAdvanced(val segment: Int) : PlaybackEvent
    data object ChapterComplete : PlaybackEvent
    data class NextChapter(val chapterIndex: Int) : PlaybackEvent
    data class PrevChapter(val chapterIndex: Int) : PlaybackEvent
    data class Failed(val message: String) : PlaybackEvent
    data object AudioFocusLoss : PlaybackEvent
    data object AudioFocusGain : PlaybackEvent
    data object BecomingNoisy : PlaybackEvent
}
