package com.novelstack.reader.tts

/**
 * 最小 WAV 解析：只处理 PCM 16-bit（服务端 mock/piper/openai 输出均满足）。
 * 返回的 PCM 直接交给 AudioTrack，不落盘。
 */
data class PcmAudio(
    val sampleRate: Int,
    val channels: Int,
    val pcm: ByteArray,
)

class WavFormatException(message: String) : Exception(message)

fun parseWav(bytes: ByteArray): PcmAudio {
    if (bytes.size < 44) throw WavFormatException("too short")
    if (tag(bytes, 0) != "RIFF" || tag(bytes, 8) != "WAVE") {
        throw WavFormatException("not a RIFF/WAVE file")
    }
    var pos = 12
    var sampleRate = 0
    var channels = 0
    var bits = 0
    var audioFormat = 0
    var dataStart = -1
    var dataLen = 0
    while (pos + 8 <= bytes.size) {
        val id = tag(bytes, pos)
        val size = le32(bytes, pos + 4)
        val body = pos + 8
        if (size < 0 || body + size > bytes.size) {
            // 有些编码器会写一个略大的 data size；只接受能截断到文件末尾的情况。
            if (id == "data") {
                dataStart = body
                dataLen = bytes.size - body
                break
            }
            throw WavFormatException("chunk $id overflows file")
        }
        when (id) {
            "fmt " -> {
                if (size < 16) throw WavFormatException("fmt chunk too short")
                audioFormat = le16(bytes, body)
                channels = le16(bytes, body + 2)
                sampleRate = le32(bytes, body + 4)
                bits = le16(bytes, body + 14)
            }
            "data" -> {
                dataStart = body
                dataLen = size
            }
        }
        pos = body + size + (size and 1) // chunks 按偶数字节对齐
    }
    if (audioFormat != 1) throw WavFormatException("unsupported audio format $audioFormat")
    if (bits != 16) throw WavFormatException("unsupported bits per sample $bits")
    if (channels !in 1..2) throw WavFormatException("unsupported channels $channels")
    if (sampleRate !in 8000..48000) throw WavFormatException("unsupported sample rate $sampleRate")
    if (dataStart < 0 || dataLen <= 0) throw WavFormatException("missing data chunk")
    return PcmAudio(sampleRate, channels, bytes.copyOfRange(dataStart, dataStart + dataLen))
}

private fun tag(b: ByteArray, off: Int): String =
    String(byteArrayOf(b[off], b[off + 1], b[off + 2], b[off + 3]), Charsets.US_ASCII)

private fun le16(b: ByteArray, off: Int): Int =
    (b[off].toInt() and 0xff) or ((b[off + 1].toInt() and 0xff) shl 8)

private fun le32(b: ByteArray, off: Int): Int =
    (b[off].toInt() and 0xff) or
        ((b[off + 1].toInt() and 0xff) shl 8) or
        ((b[off + 2].toInt() and 0xff) shl 16) or
        ((b[off + 3].toInt() and 0xff) shl 24)
