package com.novelstack.reader.reading

import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class PrefetchGateTest {

    @Test
    fun belowThresholdDoesNotTrigger() {
        val gate = PrefetchGate(0.8f)
        assertFalse(gate.shouldTrigger(chapterIndex = 3, progress = 0.5f, hasNext = true))
        assertFalse(gate.shouldTrigger(chapterIndex = 3, progress = 0.79f, hasNext = true))
    }

    @Test
    fun atOrAboveThresholdTriggersOnce() {
        val gate = PrefetchGate(0.8f)
        assertTrue(gate.shouldTrigger(3, 0.8f, true))
        // 同一章不再重复触发
        assertFalse(gate.shouldTrigger(3, 0.85f, true))
        assertFalse(gate.shouldTrigger(3, 1.0f, true))
    }

    @Test
    fun reArmsForNextChapter() {
        val gate = PrefetchGate(0.8f)
        assertTrue(gate.shouldTrigger(3, 0.9f, true))
        assertTrue(gate.shouldTrigger(4, 0.9f, true))
    }

    @Test
    fun noNextChapterNeverTriggers() {
        val gate = PrefetchGate(0.8f)
        assertFalse(gate.shouldTrigger(9, 0.99f, false))
    }

    @Test
    fun resetAllowsRetrigger() {
        val gate = PrefetchGate(0.8f)
        assertTrue(gate.shouldTrigger(2, 0.9f, true))
        assertFalse(gate.shouldTrigger(2, 0.9f, true))
        gate.reset()
        assertTrue(gate.shouldTrigger(2, 0.9f, true))
    }
}
