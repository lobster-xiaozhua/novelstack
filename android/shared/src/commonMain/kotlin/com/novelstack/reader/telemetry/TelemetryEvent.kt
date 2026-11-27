package com.novelstack.reader.telemetry

/**
 * 遥测事件模型与类型白名单。
 *
 * 红线：本类型只承载运行状况、设备信息与耗时。
 * 书籍正文、音频明文、Token/密钥永远不进 [context]，入口处 [TelemetrySanitizer] 会再拦一次。
 */
object TelemetryKind {
    const val CRASH = "CRASH"
    const val ANR = "ANR"
    const val SLOW_CHAPTER = "SLOW_CHAPTER"
    const val TTS_FIRST_FRAME = "TTS_FIRST_FRAME"
    const val JANK = "JANK"
    const val PRELOAD_STATS = "PRELOAD_STATS"
    const val SEARCH_STATS = "SEARCH_STATS"
    const val API = "API"
    const val COLD_START = "COLD_START"
    const val PLAYBACK_ERROR = "PLAYBACK_ERROR"
    const val FOREGROUND_SYNC = "FOREGROUND_SYNC"

    val all = setOf(
        CRASH, ANR, SLOW_CHAPTER, TTS_FIRST_FRAME, JANK, PRELOAD_STATS,
        SEARCH_STATS, API, COLD_START, PLAYBACK_ERROR, FOREGROUND_SYNC,
    )

    /** 崩溃类事件在淘汰时必须保留最近 24 小时，不受条数/体积上限影响。 */
    fun isCrash(kind: String): Boolean = kind == CRASH || kind == ANR
}

/** 清洗后待落库的事件。context 只保留标量字符串，序列化后体积可控。 */
data class TelemetryEvent(
    val kind: String,
    val name: String,
    val severity: String,
    val ts: Long,
    val durationMs: Long,
    val stack: String,
    val context: Map<String, String>,
    val count: Long,
    val fingerprint: String,
    val bytes: Long,
)

/** 队列里的一行（带自增 id，上传成功后按 id 删除）。 */
data class QueuedEvent(
    val id: Long,
    val kind: String,
    val name: String,
    val severity: String,
    val ts: Long,
    val durationMs: Long,
    val stack: String,
    val context: String,
    val count: Long,
    val deviceId: String,
    val appVersion: String,
    val osVersion: String,
    val model: String,
)
