package com.novelstack.reader.telemetry

import com.novelstack.reader.net.NovelApi
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject

/**
 * 批量上传：把队列里的记录拼成服务端约定的 envelope，gzip 后 POST。
 *
 * 只负责「一次尝试」，重试策略由 WorkManager 负责（Result.retry + 指数退避）。
 * 成功后立刻按 id 删除本地记录——上传与删除在同一个调用里完成，避免重复上报。
 */
class TelemetryUploader(
    private val api: NovelApi,
    private val repo: TelemetryRepository,
    private val json: Json,
) {
    @Serializable
    private data class Envelope(
        val device_id: String,
        val app_version: String,
        val os: String,
        val model: String,
        val events: List<WireEvent>,
    )

    @Serializable
    private data class WireEvent(
        val id: String,
        val kind: String,
        val name: String,
        val severity: String,
        val ts: String,
        val duration_ms: Long,
        val stack: String,
        val context: JsonObject,
        val count: Long,
    )

    /**
     * 返回本次上传成功的条数；队列为空返回 0。
     * 网络错误/鉴权失败直接抛出，由调用方决定是否重试。
     */
    suspend fun uploadOnce(): Int {
        val rows = repo.pending()
        if (rows.isEmpty()) return 0
        val first = rows.first()
        val env = Envelope(
            device_id = first.deviceId,
            app_version = first.appVersion,
            os = first.osVersion,
            model = first.model,
            events = rows.map { r ->
                WireEvent(
                    id = r.id.toString(),
                    kind = r.kind,
                    name = r.name,
                    severity = r.severity,
                    ts = Telemetry.isoTime(r.ts),
                    duration_ms = r.durationMs,
                    stack = r.stack,
                    context = TelemetrySanitizer.decodeContext(r.context),
                    count = r.count,
                )
            },
        )
        val payload = json.encodeToString(Envelope.serializer(), env).encodeToByteArray()
        api.uploadTelemetry(gzipCompress(payload))
        repo.markUploaded(rows.map { it.id })
        return rows.size
    }

    /** 本地过期清理（7 天），每次上传前后各跑一次，成本极低。 */
    fun cleanupLocal(now: Long = Telemetry.now()): Long = repo.cleanup(now)
}
