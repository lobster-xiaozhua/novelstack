package com.novelstack.reader.telemetry

import kotlinx.datetime.Clock
import kotlinx.datetime.Instant

/**
 * 全局遥测入口。
 *
 * 用法约束：
 *  - 只允许上报运行状况、设备信息与耗时；
 *  - [record] 是**同步**写库（Sink 内部直接执行 SQL），崩溃路径可安全调用；
 *  - 任何异常都在内部吞掉，遥测自身绝不能让业务崩溃。
 */
object Telemetry {

    /** 平台实现：负责同步落盘与触发上传。 */
    interface Sink {
        /** 同步写入一条事件。实现必须阻塞到 SQL 提交完成。 */
        fun insertSync(e: TelemetryEvent)

        /** 请求一次受约束保护的上传（WorkManager）。 */
        fun requestUpload()

        fun deviceMeta(): DeviceMeta

        /** 登录/配对完成后由 AppState 注入，之后所有事件都带上设备标识。 */
        fun updateDeviceId(id: String)
    }

    @Volatile
    private var sink: Sink? = null

    @Volatile
    var enabled: Boolean = true

    fun install(s: Sink?) {
        sink = s
    }

    fun now(): Long = Clock.System.now().toEpochMilliseconds()

    /**
     * 记录一条事件。参数保持窄类型（Map<String,String>），从类型上就杜绝把正文塞进来。
     */
    fun record(
        kind: String,
        name: String,
        durationMs: Long = 0,
        stack: String = "",
        context: Map<String, String> = emptyMap(),
        severity: String? = null,
        count: Long = 1,
    ) {
        if (!enabled) return
        val s = sink ?: return
        runCatching {
            val e = TelemetrySanitizer.build(
                kind = kind,
                name = name,
                ts = now(),
                durationMs = durationMs,
                stack = stack,
                context = context,
                count = count,
                severity = severity,
            ) ?: return
            s.insertSync(e)
        }
    }

    /** 记录未捕获异常：完整堆栈 + 进程/线程上下文（不含任何用户内容）。 */
    fun crash(t: Throwable, where: String = "uncaught") {
        record(
            kind = TelemetryKind.CRASH,
            name = t::class.simpleName ?: t.javaClass.name,
            stack = t.stackTraceToString(),
            context = mapOf(
                "where" to where,
                "message_class" to (t.message?.let { "present" } ?: "null"),
            ),
        )
    }

    fun crash(message: String, stack: String, where: String) {
        record(
            kind = TelemetryKind.CRASH,
            name = message.take(160),
            stack = stack,
            context = mapOf("where" to where),
        )
    }

    /** 测量一段代码耗时，超过阈值才上报（避免无意义的高频埋点）。 */
    inline fun <T> timed(
        kind: String,
        name: String,
        thresholdMs: Long = 0,
        context: () -> Map<String, String> = { emptyMap() },
        block: () -> T,
    ): T {
        val start = now()
        try {
            return block()
        } finally {
            val cost = now() - start
            if (cost >= thresholdMs) {
                record(kind, name, durationMs = cost, context = context())
            }
        }
    }

    fun requestUpload() {
        runCatching { sink?.requestUpload() }
    }

    fun meta(): DeviceMeta? = sink?.deviceMeta()

    fun updateDeviceId(id: String) {
        runCatching { sink?.updateDeviceId(id) }
    }

    fun isoTime(ms: Long): String = Instant.fromEpochMilliseconds(ms).toString()
}

/** 平台层 gzip；服务端按 Content-Encoding: gzip 解压。 */
expect fun gzipCompress(data: ByteArray): ByteArray
