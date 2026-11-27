package com.novelstack.reader.telemetry

import androidx.work.NetworkType
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** 分级策略验收：上传任务必须被 UNMETERED 网络约束保护（移动网络下不会执行）。 */
class TelemetryWorkTest {

    @Test
    fun uploadOnlyRunsOnUnmeteredNetwork() {
        val c = TelemetryWork.constraints()
        assertEquals(NetworkType.UNMETERED, c.requiredNetworkType, "必须只在 WiFi/以太网执行")
        // 低电量不再用 batteryNotLow 一刀切（否则连崩溃都传不出去），改由 worker 内降级为「只传崩溃」。
        assertFalse(c.requiresBatteryNotLow(), "低电量应由 worker 判断后降级，而不是直接卡住任务")
    }

    @Test
    fun lowPowerPolicyOnlyThrottlesNonFatalUploads() {
        // 20% 以下、未充电 → 低电量
        assertTrue(TelemetryWork.isLowPower(charging = false, percent = 12, powerSaveMode = false))
        // 省电模式即便电量不低也算
        assertTrue(TelemetryWork.isLowPower(charging = false, percent = 80, powerSaveMode = true))
        // 充电中不算低电量
        assertFalse(TelemetryWork.isLowPower(charging = true, percent = 5, powerSaveMode = false))
        // 正常电量
        assertFalse(TelemetryWork.isLowPower(charging = false, percent = 50, powerSaveMode = false))
        // 0 多为读取失败，按非低电量处理，避免把正常设备误降级
        assertFalse(TelemetryWork.isLowPower(charging = false, percent = 0, powerSaveMode = false))
    }

    @Test
    fun policyMatchesDocumentedLimits() {
        assertEquals(1000, TelemetryPolicy.MAX_ROWS)
        assertEquals(5L * 1024 * 1024, TelemetryPolicy.MAX_BYTES)
        assertEquals(24L * 60 * 60 * 1000, TelemetryPolicy.KEEP_CRASH_MS)
        assertEquals(7L * 24 * 60 * 60 * 1000, TelemetryPolicy.LOCAL_RETENTION_MS)
    }
}
