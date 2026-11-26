package com.novelstack.reader.tts

import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith

/**
 * WAV 解析测试：服务端音频段解出来就是 16-bit PCM WAV，
 * 解析结果直接喂给 AudioTrack；解析错了会听到噪音或直接崩。
 */
class WavTest {

    @Test
    fun parsesPcm16Mono() {
        val pcm = ByteArray(160) { i -> (i % 128).toByte() }
        val wav = buildWav(pcm, sampleRate = 24000, channels = 1, bits = 16)
        val audio = parseWav(wav)
        assertEquals(24000, audio.sampleRate)
        assertEquals(1, audio.channels)
        assertContentEquals(pcm, audio.pcm)
    }

    @Test
    fun parsesStereo44k() {
        val pcm = ByteArray(400)
        val audio = parseWav(buildWav(pcm, sampleRate = 44100, channels = 2, bits = 16))
        assertEquals(44100, audio.sampleRate)
        assertEquals(2, audio.channels)
        assertEquals(400, audio.pcm.size)
    }

    @Test
    fun skipsUnknownChunksWithPadding() {
        // 一个奇数长度的 LIST 块（后面要补 1 字节对齐）不能打断解析
        val pcm = ByteArray(64) { 7 }
        val base = buildWav(pcm, sampleRate = 16000, channels = 1, bits = 16)
        val fmtEnd = 12 + 8 + 16          // RIFF 头 + fmt 块
        val head = base.copyOfRange(0, fmtEnd)
        val tail = base.copyOfRange(fmtEnd, base.size)
        val list = "LIST".encodeToByteArray() + le32(3) + byteArrayOf(1, 2, 3) + byteArrayOf(0)
        val merged = head + list + tail
        // RIFF size 需要同步修正，否则部分严格解析器会报错（本实现不校验 RIFF size）
        val audio = parseWav(merged)
        assertContentEquals(pcm, audio.pcm)
    }

    @Test
    fun toleratesOversizedDataChunk() {
        // 某些编码器把 data size 写成比实际更大：截断到文件末尾即可
        val pcm = ByteArray(32) { 1 }
        val wav = buildWav(pcm, sampleRate = 24000, channels = 1, bits = 16)
        val dataLenOff = wav.size - pcm.size - 4
        le32(pcm.size + 4096).copyInto(wav, dataLenOff)
        assertEquals(32, parseWav(wav).pcm.size)
    }

    @Test
    fun rejectsUnsupportedFormats() {
        val pcm = ByteArray(16)
        assertFailsWith<WavFormatException> {
            parseWav(buildWav(pcm, sampleRate = 24000, channels = 1, bits = 8))
        }
        assertFailsWith<WavFormatException> {
            parseWav(buildWav(pcm, sampleRate = 24000, channels = 1, bits = 24))
        }
        assertFailsWith<WavFormatException> {
            parseWav(buildWav(pcm, sampleRate = 96000, channels = 1, bits = 16))
        }
        assertFailsWith<WavFormatException> {
            parseWav(buildWav(pcm, sampleRate = 24000, channels = 1, bits = 16, audioFormat = 3))
        }
        assertFailsWith<WavFormatException> { parseWav(ByteArray(10)) }
        assertFailsWith<WavFormatException> { parseWav("NOPEnope".encodeToByteArray() + ByteArray(64)) }
    }

    // ---- 辅助 ----

    private fun buildWav(
        pcm: ByteArray,
        sampleRate: Int,
        channels: Int,
        bits: Int,
        audioFormat: Int = 1,
    ): ByteArray {
        val blockAlign = channels * bits / 8
        val byteRate = sampleRate * blockAlign
        val out = ArrayList<Byte>(44 + pcm.size)
        out += "RIFF".encodeToByteArray().toList()
        out += le32(36 + pcm.size).toList()
        out += "WAVE".encodeToByteArray().toList()
        out += "fmt ".encodeToByteArray().toList()
        out += le32(16).toList()
        out += le16(audioFormat).toList()
        out += le16(channels).toList()
        out += le32(sampleRate).toList()
        out += le32(byteRate).toList()
        out += le16(blockAlign).toList()
        out += le16(bits).toList()
        out += "data".encodeToByteArray().toList()
        out += le32(pcm.size).toList()
        out += pcm.toList()
        return out.toByteArray()
    }

    private fun le16(v: Int) = byteArrayOf((v and 0xff).toByte(), ((v ushr 8) and 0xff).toByte())

    private fun le32(v: Int) = byteArrayOf(
        (v and 0xff).toByte(),
        ((v ushr 8) and 0xff).toByte(),
        ((v ushr 16) and 0xff).toByte(),
        ((v ushr 24) and 0xff).toByte(),
    )
}
