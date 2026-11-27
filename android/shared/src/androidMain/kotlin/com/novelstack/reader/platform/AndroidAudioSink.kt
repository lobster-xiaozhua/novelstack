package com.novelstack.reader.platform

import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioTrack
import android.media.PlaybackParams
import com.novelstack.reader.app.AudioBufferPolicy

/**
 * 用 AudioTrack 顺序播放 16-bit PCM。
 *
 * 数据来源是内存中的解密结果（TTS 片段），本类不读写任何文件。
 * 语速通过 PlaybackParams 调整（时间伸缩，不变调）。
 */
class AndroidAudioSink : AudioSink {
    private var track: AudioTrack? = null

    override fun start(sampleRate: Int, channels: Int) {
        stop()
        val channelMask = if (channels <= 1) {
            AudioFormat.CHANNEL_OUT_MONO
        } else {
            AudioFormat.CHANNEL_OUT_STEREO
        }
        val minBuf = AudioTrack.getMinBufferSize(
            sampleRate, channelMask, AudioFormat.ENCODING_PCM_16BIT,
        ).coerceAtLeast(16 * 1024)
        val t = AudioTrack.Builder()
            .setAudioAttributes(
                AudioAttributes.Builder()
                    .setUsage(AudioAttributes.USAGE_MEDIA)
                    .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH)
                    .build(),
            )
            .setAudioFormat(
                AudioFormat.Builder()
                    .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                    .setSampleRate(sampleRate)
                    .setChannelMask(channelMask)
                    .build(),
            )
            .setTransferMode(AudioTrack.MODE_STREAM)
            .setBufferSizeInBytes(minBuf * AudioBufferPolicy.multiplier())
            .build()
        track = t
        t.play()
    }

    override fun write(pcm: ByteArray) {
        val t = track ?: return
        var off = 0
        while (off < pcm.size) {
            val n = t.write(pcm, off, pcm.size - off)
            if (n <= 0) break
            off += n
        }
    }

    override fun pause() {
        runCatching { track?.pause() }
    }

    override fun resume() {
        runCatching { track?.play() }
    }

    override fun stop() {
        track?.let {
            runCatching { it.pause() }
            runCatching { it.flush() }
            runCatching { it.stop() }
            runCatching { it.release() }
        }
        track = null
    }

    override fun setSpeed(speed: Float) {
        val t = track ?: return
        runCatching {
            t.playbackParams = PlaybackParams().setSpeed(speed.coerceIn(0.5f, 2.5f))
        }
    }
}

class AndroidAudioSinkFactory : AudioSinkFactory {
    override fun create(): AudioSink = AndroidAudioSink()
}
