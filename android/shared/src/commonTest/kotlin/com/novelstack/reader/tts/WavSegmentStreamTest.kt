package com.novelstack.reader.tts

import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith

/**
 * 自定义流读取器测试：
 *  - 剥离每个片段独立的 RIFF 头，拼出连续 PCM；
 *  - HTTP chunk 边界与片段边界不对齐时仍能正确解析；
 *  - 格式不一致、损坏流要被拒绝。
 */
class WavSegmentStreamTest {

    @Test
    fun stripsRiffHeaderFromSingleSegment() {
        val pcm = ByteArray(64) { (it % 100).toByte() }
        val stream = WavSegmentStream()
        val blocks = stream.feedAndTake(buildWav(pcm, 24000, 1))
        assertEquals(1, blocks.size)
        assertEquals(24000, blocks[0].sampleRate)
        assertEquals(1, blocks[0].channels)
        assertContentEquals(pcm, blocks[0].pcm)
    }

    @Test
    fun joinsTwoSegmentsIntoContinuousPcm() {
        val pcmA = ByteArray(32) { 1 }
        val pcmB = ByteArray(48) { 2 }
        val stream = WavSegmentStream()
        val blocks = stream.feedAndTake(buildWav(pcmA, 24000, 1) + buildWav(pcmB, 24000, 1))
        assertEquals(2, blocks.size)
        assertContentEquals(pcmA + pcmB, joinPcm(blocks)!!.pcm)
    }

    @Test
    fun handlesChunksSplitAcrossArbitraryBoundaries() {
        // 逐字节喂入，模拟最坏情况的 TCP/HTTP 分片。
        val pcmA = ByteArray(40) { (it * 3).toByte() }
        val pcmB = ByteArray(36) { (it * 5).toByte() }
        val wire = buildWav(pcmA, 16000, 1) + buildWav(pcmB, 16000, 1)
        val stream = WavSegmentStream()
        val collected = ArrayList<Byte>()
        for (byte in wire) {
            val blocks = stream.feedAndTake(byteArrayOf(byte))
            for (b in blocks) b.pcm.forEach { collected += it }
        }
        stream.endOfStream()
        assertContentEquals(pcmA + pcmB, collected.toByteArray())
    }

    @Test
    fun handlesOddLengthChunksWithPadding() {
        // 一个 3 字节的 data（奇数）后跟 1 字节 padding，必须被正确跳过。
        val wav = buildWav(ByteArray(3) { 9 }, 24000, 1)
        val stream = WavSegmentStream()
        val blocks = stream.feedAndTake(wav)
        assertEquals(1, blocks.size)
        assertEquals(3, blocks[0].pcm.size)
    }

    @Test
    fun rejectsFormatMismatchAcrossSegments() {
        val stream = WavSegmentStream()
        val first = stream.feedAndTake(buildWav(ByteArray(16), 24000, 1))
        assertEquals(1, first.size)
        assertFailsWith<WavFormatException> {
            stream.feedAndTake(buildWav(ByteArray(16), 48000, 1))
        }
    }

    @Test
    fun rejectsBrokenRiffHeader() {
        val stream = WavSegmentStream()
        assertFailsWith<WavFormatException> {
            stream.feedAndTake("NOPE".encodeToByteArray() + ByteArray(40))
        }
    }

    @Test
    fun detectsTruncatedStream() {
        val stream = WavSegmentStream()
        val wav = buildWav(ByteArray(32), 24000, 1)
        stream.feedAndTake(wav.copyOfRange(0, wav.size - 3))
        assertFailsWith<WavFormatException> { stream.endOfStream() }
    }

    // ---- 辅助 ----

    private fun WavSegmentStream.feedAndTake(bytes: ByteArray): List<WavSegmentStream.PcmBlock> {
        feed(bytes)
        return takePcm()
    }

    private fun buildWav(
        pcm: ByteArray,
        sampleRate: Int,
        channels: Int,
        bits: Int = 16,
    ): ByteArray {
        val blockAlign = channels * bits / 8
        val byteRate = sampleRate * blockAlign
        val out = ArrayList<Byte>(44 + pcm.size)
        out += "RIFF".encodeToByteArray().toList()
        out += le32(36 + pcm.size).toList()
        out += "WAVE".encodeToByteArray().toList()
        out += "fmt ".encodeToByteArray().toList()
        out += le32(16).toList()
        out += le16(1).toList()
        out += le16(channels).toList()
        out += le32(sampleRate).toList()
        out += le32(byteRate).toList()
        out += le16(blockAlign).toList()
        out += le16(bits).toList()
        out += "data".encodeToByteArray().toList()
        out += le32(pcm.size).toList()
        out += pcm.toList()
        if ((pcm.size and 1) == 1) out += 0 // WAV spec：奇数长度 chunk 后补 1 字节对齐
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
