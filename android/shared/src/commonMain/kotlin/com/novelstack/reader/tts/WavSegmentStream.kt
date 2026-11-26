package com.novelstack.reader.tts

import kotlin.math.min

/**
 * 自定义 WAV 片段流读取器（第七阶段核心）。
 *
 * 背景：服务端 `Content-Type: audio/wav` + `X-TTS-Stream: wav-segments` 的响应体是
 * 若干个【各自带独立 RIFF/WAVE 头】的 WAV 片段顺序拼接而成的字节流。标准 MediaPlayer
 * 只认一个 RIFF 头，无法直接播放。本读取器按字节增量消费该流，逐个识别并剥离每个片段
 * 的 RIFF/fmt/data 头，仅把 data 块的 PCM 依次送出。
 *
 * 设计要点：
 *  - 纯 Kotlin、纯内存：内部只有一个 ByteArray 暂存区，绝不落盘；
 *  - 增量（streaming）：HTTP chunk 边界与 WAV 片段边界不对齐也能正确解析，
 *    [feed] 可喂任意切片，[takePcm] 返回当前已凑齐的完整片段 PCM；
 *  - 只接受 16-bit PCM（服务端各引擎输出均满足），格式不一致抛 [WavFormatException]。
 *
 * 注意：本类消费的是【已解密】的 WAV 字节。密文解密由上层（TtsController）在内存中完成，
 * 解密结果直接 feed 进来，全程不写文件。
 */
class WavSegmentStream(
    private val maxSegmentBytes: Int = DEFAULT_MAX_SEGMENT,
) {
    companion object {
        const val DEFAULT_MAX_SEGMENT = 16 * 1024 * 1024 // 单片段上限，防御损坏流导致 OOM
    }

    /** 一个片段解出的 PCM 数据及其格式（与后续片段应保持一致）。 */
    data class PcmBlock(
        val sampleRate: Int,
        val channels: Int,
        val pcm: ByteArray,
    )

    private enum class Phase {
        RIFF_ID, RIFF_SIZE, WAVE_ID,
        CHUNK_ID, CHUNK_SIZE, CHUNK_BODY,
    }

    // 暂存尚未解析完的字节；consume 后前移，必要时 compact。
    private var buf = ByteArray(0)
    private var start = 0

    private var phase = Phase.RIFF_ID
    private var chunkId = ""
    private var chunkSize = 0

    // 当前片段的 fmt 信息（在 data 块之前出现）。
    private var fmtRate = 0
    private var fmtChannels = 0
    private var fmtBits = 0
    private var fmtAudioFormat = 0

    // 首个成功片段确定的全局格式，用于校验后续片段一致性。
    private var streamRate = 0
    private var streamChannels = 0

    /** 目前暂存区中尚未解析的字节数（调试/诊断用，绝不输出内容）。 */
    fun bufferedBytes(): Int = buf.size - start

    /** 喂入一段字节流（可跨越任意片段边界）。 */
    fun feed(bytes: ByteArray) {
        if (bytes.isEmpty()) return
        if (start > 0) compact()
        buf = buf + bytes
    }

    /**
     * 取出当前已凑齐的 PCM 片段列表。可能返回 0 个（数据不够）、1 个或多个
     * （一次 feed 里包含多个完整片段）。返回的 ByteArray 是新分配副本，调用方可安全持有。
     */
    fun takePcm(): List<PcmBlock> {
        val out = ArrayList<PcmBlock>()
        var progressed = true
        while (progressed) {
            progressed = when (phase) {
                Phase.RIFF_ID -> readTag("RIFF") { phase = Phase.RIFF_SIZE }
                Phase.RIFF_SIZE -> skipIfAvailable(4) { phase = Phase.WAVE_ID }
                Phase.WAVE_ID -> readTag("WAVE") { phase = Phase.CHUNK_ID }
                Phase.CHUNK_ID -> {
                    if (available() < 4) {
                        false
                    } else {
                        val tag = ascii(4)
                        if (tag == "RIFF") {
                            // 新片段的开始：跳回 RIFF_SIZE（WAVE 头紧随其后）。
                            phase = Phase.RIFF_SIZE
                        } else {
                            chunkId = tag
                            phase = Phase.CHUNK_SIZE
                        }
                        true
                    }
                }
                Phase.CHUNK_SIZE -> {
                    if (available() < 4) {
                        false
                    } else {
                        chunkSize = le32()
                        if (chunkSize < 0) throw WavFormatException("negative chunk size")
                        phase = Phase.CHUNK_BODY
                        true
                    }
                }
                Phase.CHUNK_BODY -> {
                    if (chunkSize > maxSegmentBytes) {
                        throw WavFormatException("chunk too large: $chunkSize")
                    }
                    val padding = chunkSize and 1
                    // 必须连同偶数字节对齐的 padding 一起凑齐，才能安全消费整个 chunk。
                    if (available() < chunkSize + padding) {
                        false
                    } else {
                        val body = copyOf(chunkSize)
                        skip(padding) { } // 丢弃对齐字节
                        phase = Phase.CHUNK_ID
                        handleChunk(chunkId, body, out)
                        true
                    }
                }
            }
        }
        if (start > 0 && start == buf.size) {
            buf = ByteArray(0)
            start = 0
        } else if (start > buf.size / 2) {
            compact()
        }
        return out
    }

    /** 输入结束：若仍有未解析完的字节则判定为损坏流。 */
    fun endOfStream() {
        if (available() > 0 || phase != Phase.CHUNK_ID) {
            throw WavFormatException("truncated wav segment stream (phase=$phase, left=${available()})")
        }
    }

    private fun handleChunk(id: String, body: ByteArray, out: MutableList<PcmBlock>) {
        when (id) {
            "fmt " -> {
                if (body.size < 16) throw WavFormatException("fmt chunk too short")
                fmtAudioFormat = le16(body, 0)
                fmtChannels = le16(body, 2)
                fmtRate = le32(body, 4)
                fmtBits = le16(body, 14)
            }
            "data" -> {
                if (fmtAudioFormat != 1) throw WavFormatException("unsupported audio format $fmtAudioFormat")
                if (fmtBits != 16) throw WavFormatException("unsupported bits per sample $fmtBits")
                if (fmtChannels !in 1..2) throw WavFormatException("unsupported channels $fmtChannels")
                if (fmtRate !in 8000..48000) throw WavFormatException("unsupported sample rate $fmtRate")
                if (streamRate == 0) {
                    streamRate = fmtRate
                    streamChannels = fmtChannels
                } else if (fmtRate != streamRate || fmtChannels != streamChannels) {
                    throw WavFormatException("segment format mismatch: $fmtRate/$fmtChannels vs $streamRate/$streamChannels")
                }
                out += PcmBlock(streamRate, streamChannels, body)
                // 每个片段以新的 RIFF 头开始，重置 fmt，避免上一片段残留。
                fmtAudioFormat = 0
                fmtRate = 0
                fmtChannels = 0
                fmtBits = 0
            }
            // RIFF/JUNK/LIST 等未知块：忽略
        }
    }

    // ---- 暂存区原语 ----

    private fun available(): Int = buf.size - start

    private inline fun skip(n: Int, after: () -> Unit): Boolean {
        start += n
        after()
        return true
    }

    /** 仅当字节足够时跳过；不足则返回 false 等待下一次 feed（用于跨 chunk 边界）。 */
    private inline fun skipIfAvailable(n: Int, after: () -> Unit): Boolean {
        if (available() < n) return false
        start += n
        after()
        return true
    }

    private inline fun readTag(expected: String, after: () -> Unit): Boolean {
        if (available() < 4) return false
        val tag = ascii(4)
        if (tag != expected) throw WavFormatException("expected $expected, got $tag")
        after()
        return true
    }

    private fun ascii(n: Int): String {
        val s = String(buf, start, n, Charsets.US_ASCII)
        start += n
        return s
    }

    private fun le32(): Int {
        val v = le32(buf, start)
        start += 4
        return v
    }

    private fun copyOf(n: Int): ByteArray {
        val out = buf.copyOfRange(start, start + n)
        start += n
        return out
    }

    private fun compact() {
        if (start == 0) return
        val rest = buf.copyOfRange(start, buf.size)
        buf = rest
        start = 0
    }

    private fun le16(b: ByteArray, off: Int): Int =
        (b[off].toInt() and 0xff) or ((b[off + 1].toInt() and 0xff) shl 8)

    private fun le32(b: ByteArray, off: Int): Int =
        (b[off].toInt() and 0xff) or
            ((b[off + 1].toInt() and 0xff) shl 8) or
            ((b[off + 2].toInt() and 0xff) shl 16) or
            ((b[off + 3].toInt() and 0xff) shl 24)

    /** 便捷：一次喂入完整 WAV 字节（内部仍走增量解析），返回解出的 PCM。 */
    fun parseOne(wav: ByteArray): PcmBlock {
        feed(wav)
        val blocks = takePcm()
        endOfStream()
        if (blocks.size != 1) throw WavFormatException("expected one segment, got ${blocks.size}")
        return blocks[0]
    }
}

/** 兼容旧代码：把多段 PCM 顺序拼接为一段（仍只在内存中）。 */
fun joinPcm(blocks: List<WavSegmentStream.PcmBlock>): WavSegmentStream.PcmBlock? {
    if (blocks.isEmpty()) return null
    val first = blocks[0]
    val total = blocks.sumOf { min(it.pcm.size, Int.MAX_VALUE) }
    val merged = ByteArray(total)
    var off = 0
    for (b in blocks) {
        b.pcm.copyInto(merged, off)
        off += b.pcm.size
    }
    return WavSegmentStream.PcmBlock(first.sampleRate, first.channels, merged)
}
