package com.novelstack.reader.telemetry

import com.novelstack.reader.telemetry.db.TelemetryDatabase

/** 设备维度信息，随每条记录落库，上传时直接组进 envelope。 */
data class DeviceMeta(
    val deviceId: String,
    val appVersion: String,
    val osVersion: String,
    val model: String,
)

/** 队列统计，供设置页展示。 */
data class TelemetryStats(val rows: Long, val bytes: Long, val crashes: Long)

/**
 * 遥测队列仓库。
 *
 * 关键约束：所有写入都是**同步**调用（调用线程直接执行 SQL），
 * 崩溃路径上绝不使用协程或内存队列，确保进程被杀前数据已落盘。
 */
class TelemetryRepository(private val db: TelemetryDatabase) {

    private val q get() = db.telemetryDatabaseQueries

    /**
     * 写入一条事件。返回 true 表示与 5 分钟内的同指纹记录合并，false 表示新建。
     */
    fun insert(e: TelemetryEvent, meta: DeviceMeta): Boolean {
        val merged = db.transactionWithResult {
            val recent = q.findRecentByFingerprint(
                e.fingerprint,
                e.ts - TelemetryPolicy.MERGE_WINDOW_MS,
            ).executeAsOneOrNull()
            if (recent != null) {
                // 合并时保留首条时间：窗口从「第一次发生」算起，避免持续报错无限延长窗口。
                q.bumpCount(e.count, recent.id)
                true
            } else {
                q.insertEvent(
                    kind = e.kind,
                    name = e.name,
                    severity = e.severity,
                    ts = e.ts,
                    duration_ms = e.durationMs,
                    stack = e.stack,
                    context = TelemetrySanitizer.encodeContext(e.context),
                    count = e.count,
                    fingerprint = e.fingerprint,
                    bytes = e.bytes,
                    device_id = meta.deviceId,
                    app_version = meta.appVersion,
                    os_version = meta.osVersion,
                    model = meta.model,
                )
                false
            }
        }
        enforceCap(System.currentTimeMillis())
        return merged
    }

    /**
     * 容量控制：超过 1000 条或 5MB 时 FIFO 淘汰，
     * 但最近 24 小时的崩溃不参与淘汰；极端情况下（全是新崩溃）允许暂时超限。
     */
    fun enforceCap(now: Long) {
        var rows = q.countRows().executeAsOne()
        var bytes = q.sumBytes().executeAsOne()
        var guard = 0
        while (TelemetryPolicy.needsEviction(rows, bytes) && guard < 200) {
            guard++
            val ids = q.oldestEvictable(
                now - TelemetryPolicy.KEEP_CRASH_MS,
                TelemetryPolicy.EVICT_CHUNK.toLong(),
            ).executeAsList()
            if (ids.isEmpty()) break
            db.transaction { ids.forEach { q.deleteById(it) } }
            rows = q.countRows().executeAsOne()
            bytes = q.sumBytes().executeAsOne()
        }
    }

    /** 取一批待上传数据，按体积上限截断（至少保留 1 条，避免大记录卡死队列）。 */
    fun pending(): List<QueuedEvent> {
        val rows = q.selectBatch(TelemetryPolicy.UPLOAD_BATCH.toLong()).executeAsList()
        val out = ArrayList<QueuedEvent>(rows.size)
        var bytes = 0L
        for (r in rows) {
            if (out.isNotEmpty() && bytes + r.bytes > TelemetryPolicy.UPLOAD_MAX_BYTES) break
            bytes += r.bytes
            out += QueuedEvent(
                id = r.id,
                kind = r.kind,
                name = r.name,
                severity = r.severity,
                ts = r.ts,
                durationMs = r.duration_ms,
                stack = r.stack,
                context = r.context,
                count = r.count,
                deviceId = r.device_id,
                appVersion = r.app_version,
                osVersion = r.os_version,
                model = r.model,
            )
        }
        return out
    }

    fun all(): List<QueuedEvent> = q.selectAll().executeAsList().map { r ->
        QueuedEvent(
            id = r.id, kind = r.kind, name = r.name, severity = r.severity, ts = r.ts,
            durationMs = r.duration_ms, stack = r.stack, context = r.context, count = r.count,
            deviceId = r.device_id, appVersion = r.app_version, osVersion = r.os_version, model = r.model,
        )
    }

    /** 上传成功后删除对应记录。 */
    fun markUploaded(ids: List<Long>) {
        if (ids.isEmpty()) return
        db.transaction { ids.forEach { q.deleteById(it) } }
    }

    /** 本地过期清理：超过 7 天仍未上传的数据直接删除。 */
    fun cleanup(now: Long): Long {
        val before = q.countRows().executeAsOne()
        q.deleteOlderThan(now - TelemetryPolicy.LOCAL_RETENTION_MS)
        return before - q.countRows().executeAsOne()
    }

    fun stats(): TelemetryStats = TelemetryStats(
        rows = q.countRows().executeAsOne(),
        bytes = q.sumBytes().executeAsOne(),
        crashes = q.countCrashes().executeAsOne(),
    )

    fun clear() = q.deleteAll()
}
