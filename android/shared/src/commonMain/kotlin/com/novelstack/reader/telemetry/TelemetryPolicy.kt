package com.novelstack.reader.telemetry

/**
 * 遥测的容量、保留与上传策略常量。集中在一处，便于测试与文档对照。
 */
object TelemetryPolicy {
    /** 同一堆栈的合并窗口：5 分钟内重复出现只累加 count。 */
    const val MERGE_WINDOW_MS = 5 * 60 * 1000L

    /** 队列上限：1000 条或 5MB，先到先淘汰。 */
    const val MAX_ROWS = 1000
    const val MAX_BYTES = 5L * 1024 * 1024

    /** 淘汰时的豁免：最近 24 小时内的崩溃/ANR 绝不删除。 */
    const val KEEP_CRASH_MS = 24 * 60 * 60 * 1000L

    /** 本地过期：超过 7 天仍未上传的数据直接删除，防止长期无 WiFi 时无限膨胀。 */
    const val LOCAL_RETENTION_MS = 7L * 24 * 60 * 60 * 1000L

    /** 单次上传条数上限。 */
    const val UPLOAD_BATCH = 200

    /** 单次上传体积上限（压缩前 JSON），避免一次请求过大。 */
    const val UPLOAD_MAX_BYTES = 512 * 1024

    /** 达到上限需要淘汰时，每次淘汰的条数（批量删比逐条快）。 */
    const val EVICT_CHUNK = 50

    /** WorkManager 唯一任务名。 */
    const val WORK_NAME = "novelstack-telemetry-upload"

    fun needsEviction(rows: Long, bytes: Long): Boolean =
        rows > MAX_ROWS || bytes > MAX_BYTES

    /**
     * 判断某条记录是否允许被淘汰。
     * 崩溃在 24 小时内是「绝对保留」，其余按 FIFO。
     */
    fun canEvict(kind: String, ts: Long, now: Long): Boolean =
        !(TelemetryKind.isCrash(kind) && now - ts < KEEP_CRASH_MS)
}
