package com.novelstack.reader.telemetry

import androidx.work.NetworkType
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/** 分级策略验收：上传任务必须被 UNMETERED + 电量约束保护（移动网络下不会执行）。 */
class TelemetryWorkTest {

    @Test
    fun uploadOnlyRunsOnUnmeteredNetwork() {
        val c = TelemetryWork.constraints()
        assertEquals(NetworkType.UNMETERED, c.requiredNetworkType, "必须只在 WiFi/以太网执行")
        assertTrue(c.requiresBatteryNotLow(), "低电量时应推迟上传")
    }

    @Test
    fun policyMatchesDocumentedLimits() {
        assertEquals(1000, TelemetryPolicy.MAX_ROWS)
        assertEquals(5L * 1024 * 1024, TelemetryPolicy.MAX_BYTES)
        assertEquals(24L * 60 * 60 * 1000, TelemetryPolicy.KEEP_CRASH_MS)
        assertEquals(7L * 24 * 60 * 60 * 1000, TelemetryPolicy.LOCAL_RETENTION_MS)
    }
}
