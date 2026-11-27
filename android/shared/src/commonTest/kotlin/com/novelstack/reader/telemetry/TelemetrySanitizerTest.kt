package com.novelstack.reader.telemetry

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue

/** 第九阶段：本地清洗的纯逻辑验收。 */
class TelemetrySanitizerTest {

    private val now = 1_700_000_000_000L

    @Test
    fun sameStackTenTimesSharesFingerprint() {
        val stack = "java.lang.IllegalStateException: boom\n\tat com.a.B.c(B.kt:12)\n\tat com.a.D.e(D.kt:34)"
        val first = TelemetrySanitizer.build(TelemetryKind.CRASH, "IllegalStateException", now, stack = stack)
        assertNotNull(first)
        repeat(9) {
            val e = TelemetrySanitizer.build(TelemetryKind.CRASH, "IllegalStateException", now, stack = stack)
            assertEquals(first.fingerprint, e!!.fingerprint)
        }
    }

    @Test
    fun lineNumbersDoNotSplitFingerprint() {
        val a = TelemetrySanitizer.build(
            TelemetryKind.CRASH, "NPE", now,
            stack = "at com.a.B.c(B.kt:12)",
        )!!
        val b = TelemetrySanitizer.build(
            TelemetryKind.CRASH, "NPE", now,
            stack = "at com.a.B.c(B.kt:99)",
        )!!
        // 发版后行号会变，但仍是同一个崩溃
        assertEquals(a.fingerprint, b.fingerprint)
    }

    @Test
    fun sensitiveContextIsDropped() {
        val e = TelemetrySanitizer.build(
            TelemetryKind.SLOW_CHAPTER, "夜航船·第一章", now,
            context = mapOf(
                "chapter_index" to "3",
                "text" to "正文不能上报",
                "token" to "nsd_abc",
                "authorization" to "Bearer x",
                "empty" to "  ",
            ),
        )!!
        assertEquals(mapOf("chapter_index" to "3"), e.context)
    }

    @Test
    fun stackIsTruncatedTo100Lines() {
        val stack = (1..300).joinToString("\n") { "at frame$it(X.kt:$it)" }
        val e = TelemetrySanitizer.build(TelemetryKind.CRASH, "Boom", now, stack = stack)!!
        val lines = e.stack.split('\n')
        assertEquals(101, lines.size) // 100 行 + 截断提示
        assertTrue(lines.last().contains("truncated"))
    }

    @Test
    fun unknownKindIsRejected() {
        assertNull(TelemetrySanitizer.build("RANDOM", "x", now))
    }

    @Test
    fun countAndDurationAreClamped() {
        val e = TelemetrySanitizer.build(
            TelemetryKind.JANK, "frames", now, durationMs = 999_999_999, count = 999_999,
        )!!
        assertEquals(10_000, e.count)
        assertEquals(24L * 60 * 60 * 1000, e.durationMs)
    }

    @Test
    fun crashSeverityIsForcedFatal() {
        val e = TelemetrySanitizer.build(TelemetryKind.ANR, "blocked", now, severity = "debug")!!
        assertEquals("fatal", e.severity)
    }
}
