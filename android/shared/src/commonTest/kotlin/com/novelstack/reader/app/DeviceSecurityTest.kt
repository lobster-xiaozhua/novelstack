package com.novelstack.reader.app

import com.novelstack.reader.reading.WordPicker
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class DeviceSecurityTest {

    @Test
    fun `low battery threshold 20 percent triggers low power`() {
        assertTrue(PowerSnapshot(19).isLow)
        assertFalse(PowerSnapshot(20).isLow)
        assertFalse(PowerSnapshot(19, isCharging = true).isLow)
        assertTrue(PowerSnapshot(50, isPowerSaveMode = true).isLow)
        assertFalse(PowerSnapshot(0).isLow) // 0 通常表示未知电量，不误判为低电
    }

    @Test
    fun `cover policy degrades on metered and low power`() {
        assertEquals(CoverLoadPolicy.Quality.FULL, CoverLoadPolicy.decide(NetworkStatus.Online, false, true))
        assertEquals(CoverLoadPolicy.Quality.LOW, CoverLoadPolicy.decide(NetworkStatus.Metered, false, true))
        assertEquals(CoverLoadPolicy.Quality.LOW, CoverLoadPolicy.decide(NetworkStatus.Offline, false, true))
        assertEquals(CoverLoadPolicy.Quality.LOW, CoverLoadPolicy.decide(NetworkStatus.Online, true, true))
        // 开关关闭后总是高清。
        assertEquals(CoverLoadPolicy.Quality.FULL, CoverLoadPolicy.decide(NetworkStatus.Metered, true, false))
    }

    @Test
    fun `audio buffer shrinks on low power`() {
        AudioBufferPolicy.lowPower = false
        assertEquals(AudioBufferPolicy.NORMAL_MULTIPLIER, AudioBufferPolicy.multiplier())
        AudioBufferPolicy.lowPower = true
        assertEquals(AudioBufferPolicy.LOW_MULTIPLIER, AudioBufferPolicy.multiplier())
        AudioBufferPolicy.lowPower = false
    }

    @Test
    fun `haptic policy throttles and respects low power`() {
        assertTrue(HapticPolicy.shouldVibrate(0, 100, enabled = true, lowPower = false))
        assertFalse(HapticPolicy.shouldVibrate(0, 10, enabled = true, lowPower = false))
        assertFalse(HapticPolicy.shouldVibrate(0, 100, enabled = false, lowPower = false))
        assertFalse(HapticPolicy.shouldVibrate(0, 100, enabled = true, lowPower = true))
    }

    @Test
    fun `word picker extracts cjk word`() {
        val text = "他转身说道：你好世界。"
        // 「你」在索引 6。
        assertEquals("你好世界", WordPicker.pick(text, 6, maxCjk = 12))
    }

    @Test
    fun `word picker extracts latin word and rejects punctuation`() {
        assertEquals("Hello", WordPicker.pick("Say Hello now", 4))
        assertEquals("", WordPicker.pick("你好，世界", 2)) // 全角逗号
    }

    @Test
    fun `word picker caps cjk length`() {
        val text = "一二三四五六七八九十甲乙丙丁"
        assertEquals("一二三四五六", WordPicker.pick(text, 0, maxCjk = 6))
    }
}
