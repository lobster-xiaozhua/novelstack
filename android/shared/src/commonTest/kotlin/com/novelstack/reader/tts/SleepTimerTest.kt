package com.novelstack.reader.tts

import kotlinx.datetime.Instant
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue
import kotlin.time.Duration.Companion.minutes
import kotlin.time.Duration.Companion.seconds

/** 用可推进的假时钟验证「到点触发」与「播完本章」两种语义。 */
class SleepTimerTest {

    private class FakeClock(var now: Instant = Instant.parse("2026-01-01T00:00:00Z")) :
        kotlinx.datetime.Clock {
        override fun now(): Instant = now
        fun advanceMinutes(n: Long) {
            now = now.plus(n.minutes)
        }
        fun advanceSeconds(n: Long) {
            now = now.plus(n.seconds)
        }
    }

    @Test
    fun countsDown15MinutesAndExpires() {
        val clock = FakeClock()
        val timer = SleepTimer(clock)
        timer.start(SleepMode.MIN_15)
        assertEquals(15 * 60_000L, timer.remainingMs())
        assertFalse(timer.tick())

        clock.advanceMinutes(14)
        assertFalse(timer.tick())
        assertTrue(timer.remainingMs() > 0)

        clock.advanceMinutes(1)
        assertTrue(timer.tick()) // 到点触发
        assertTrue(timer.tick() == false) // 只触发一次
        assertEquals(0L, timer.remainingMs())
    }

    @Test
    fun supports30And60Minutes() {
        val clock = FakeClock()
        val timer = SleepTimer(clock)
        timer.start(SleepMode.MIN_30)
        assertEquals(30 * 60_000L, timer.remainingMs())
        clock.advanceMinutes(30)
        assertTrue(timer.tick())

        timer.start(SleepMode.MIN_60)
        assertEquals(60 * 60_000L, timer.remainingMs())
        clock.advanceMinutes(59)
        assertFalse(timer.tick())
        clock.advanceMinutes(1)
        assertTrue(timer.tick())
    }

    @Test
    fun endOfChapterTriggersOnlyOnChapterEnd() {
        val clock = FakeClock()
        val timer = SleepTimer(clock)
        timer.start(SleepMode.END_OF_CHAPTER)
        // 即使时间过了 2 小时也不会按时间触发
        clock.advanceMinutes(120)
        assertFalse(timer.tick())
        assertEquals(0L, timer.remainingMs())
        // 章节结束才触发
        assertTrue(timer.onChapterEnded())
        assertFalse(timer.onChapterEnded())
    }

    @Test
    fun offAndCancelDoNothing() {
        val clock = FakeClock()
        val timer = SleepTimer(clock)
        assertFalse(timer.tick())
        assertFalse(timer.onChapterEnded())

        timer.start(SleepMode.MIN_15)
        timer.cancel()
        clock.advanceMinutes(20)
        assertFalse(timer.tick())
        assertEquals(SleepMode.OFF, timer.mode())
    }

    @Test
    fun restartResetsDeadline() {
        val clock = FakeClock()
        val timer = SleepTimer(clock)
        timer.start(SleepMode.MIN_15)
        clock.advanceMinutes(10)
        timer.start(SleepMode.MIN_15) // 重新计时
        assertEquals(15 * 60_000L, timer.remainingMs())
        clock.advanceMinutes(5)
        assertFalse(timer.tick())
        clock.advanceMinutes(10)
        assertTrue(timer.tick())
    }
}
