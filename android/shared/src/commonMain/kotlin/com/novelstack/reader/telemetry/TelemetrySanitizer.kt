package com.novelstack.reader.telemetry

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonObject

/**
 * 本地清洗与降噪：客户端的第一道（也是最重要的一道）防线。
 *
 * 规则：
 *  1. 类型不在白名单 → 丢弃；
 *  2. 堆栈超过 100 行或 16KB → 截断（第三方库深栈是体积大头）；
 *  3. context 只保留标量，敏感键（text/token/body/...）整条删除，空值丢弃；
 *  4. 同一 (kind, name, 首帧) 在 5 分钟内合并为一条，count 累加。
 *
 * 纯函数实现，便于单测；不依赖 Android。
 */
object TelemetrySanitizer {

    private const val MAX_STACK_LINES = 100
    private const val MAX_STACK_BYTES = 16 * 1024
    private const val MAX_CONTEXT_KEYS = 16
    private const val MAX_VALUE_LEN = 200
    private const val MAX_NAME_LEN = 160

    /** 命中即整条丢弃的键片段。 */
    private val sensitiveParts = listOf(
        "token", "password", "passwd", "secret", "authorization", "cookie",
        "ciphertext", "cipher", "wrapped_dek", "dek", "key",
        "content", "text", "body", "plain", "audio",
    )

    private val json = Json { encodeDefaults = false }

    /**
     * 构造一条可落库的事件。返回 null 表示这条事件非法（类型不认识等），应直接丢弃。
     */
    fun build(
        kind: String,
        name: String,
        ts: Long,
        durationMs: Long = 0,
        stack: String = "",
        context: Map<String, String> = emptyMap(),
        count: Long = 1,
        severity: String? = null,
    ): TelemetryEvent? {
        if (kind !in TelemetryKind.all) return null
        val cleanStack = truncateStack(stack)
        val cleanContext = cleanContext(context)
        val ctxJson = encodeContext(cleanContext)
        val cleanName = clip(name.trim(), MAX_NAME_LEN)
        val sev = when {
            TelemetryKind.isCrash(kind) -> "fatal"
            severity != null && severity in setOf("debug", "info", "warn", "error", "fatal") -> severity
            else -> "info"
        }
        val dur = durationMs.coerceIn(0L, 24L * 60 * 60 * 1000)
        val cnt = count.coerceIn(1L, 10_000L)
        val fp = fingerprint(kind, cleanName, cleanStack)
        val bytes = (cleanStack.length + ctxJson.length + cleanName.length + 96).toLong()
        return TelemetryEvent(
            kind = kind,
            name = cleanName,
            severity = sev,
            ts = ts,
            durationMs = dur,
            stack = cleanStack,
            context = cleanContext,
            count = cnt,
            fingerprint = fp,
            bytes = bytes,
        )
    }

    /**
     * 合并指纹：类型 + 名称 + 堆栈首帧（去掉行号，避免同一崩溃因发版行号变化而分裂）。
     * 首帧缺失时退化为「类型 + 名称」。
     */
    fun fingerprint(kind: String, name: String, stack: String): String {
        val firstFrame = stack.lineSequence()
            .map { it.trim() }
            .firstOrNull { it.startsWith("at ") || it.contains("(") }
            .orEmpty()
        val normalized = firstFrame
            .replace(Regex(":\\d+\\)"), ")")
            .replace(Regex("\\(.*?\\)"), "")
            .take(120)
        return "$kind|$name|$normalized"
    }

    fun truncateStack(stack: String): String {
        if (stack.isBlank()) return ""
        val lines = stack.replace("\r\n", "\n").split('\n')
        val kept = if (lines.size > MAX_STACK_LINES) {
            lines.take(MAX_STACK_LINES) + "… (truncated)"
        } else {
            lines
        }
        var out = kept.joinToString("\n")
        if (out.length > MAX_STACK_BYTES) {
            // 按字符截断即可：UTF-8 前缀截断只会损坏最后一个字符，服务端会再兜一层。
            out = out.take(MAX_STACK_BYTES) + "\n… (truncated)"
        }
        return out
    }

    fun cleanContext(ctx: Map<String, String>): Map<String, String> {
        if (ctx.isEmpty()) return emptyMap()
        val out = LinkedHashMap<String, String>()
        for ((k, v) in ctx) {
            if (out.size >= MAX_CONTEXT_KEYS) break
            val key = k.trim().lowercase()
            if (key.isEmpty() || isSensitive(key)) continue
            val value = v.trim()
            if (value.isEmpty()) continue
            out[key] = clip(value, MAX_VALUE_LEN)
        }
        return out
    }

    fun encodeContext(ctx: Map<String, String>): String {
        if (ctx.isEmpty()) return "{}"
        return json.encodeToString(JsonObject.serializer(), JsonObject(ctx.mapValues { JsonPrimitive(it.value) }))
    }

    /** 反解存储的 context（仅用于上传载荷拼装与测试断言）。 */
    fun decodeContext(raw: String): JsonObject = runCatching { json.parseToJsonElement(raw).jsonObject }.getOrElse { JsonObject(emptyMap()) }

    fun isSensitive(key: String): Boolean = sensitiveParts.any { key.contains(it) }

    private fun clip(s: String, max: Int): String =
        if (s.length <= max) s else s.substring(0, max)
}
