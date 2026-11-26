package com.novelstack.reader.tts

import com.novelstack.reader.crypto.AesGcm
import com.novelstack.reader.crypto.zeroBytes
import com.novelstack.reader.model.AudioManifest
import com.novelstack.reader.net.NovelApi
import com.novelstack.reader.platform.AudioSink
import com.novelstack.reader.platform.AudioSinkFactory
import com.novelstack.reader.platform.DeviceKeyStore
import io.ktor.util.decodeBase64Bytes
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

/**
 * TTS 离线播放控制器。
 *
 * 数据流（全部在内存中）：
 *   服务端 NSAC 密文 ──RSA 解包音频 DEK──> AES-GCM 解密 ──> WAV 解析 ──> AudioTrack
 *
 * 不写任何临时音频文件；暂停/继续直接作用于 AudioTrack。
 */
class TtsController(
    private val api: NovelApi,
    private val keyStore: DeviceKeyStore,
    private val sinkFactory: AudioSinkFactory,
    private val scope: CoroutineScope,
) {
    data class State(
        val playing: Boolean = false,
        val paused: Boolean = false,
        val bookId: String = "",
        val chapterIndex: Int = -1,
        val segment: Int = 0,
        val total: Int = 0,
        val speed: Float = 1.0f,
        val error: String? = null,
    )

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state

    private var job: Job? = null
    private var sink: AudioSink? = null
    private var manifest: AudioManifest? = null

    fun play(bookId: String, chapterIndex: Int, speed: Float) {
        stop()
        _state.value = State(
            playing = true, bookId = bookId, chapterIndex = chapterIndex, speed = speed,
        )
        job = scope.launch(Dispatchers.Default) {
            try {
                val s = speed.coerceIn(0.5f, 2.0f)
                api.prepareAudio(bookId, chapterIndex, s)
                val m = api.audioManifest(bookId, chapterIndex)
                manifest = m
                if (m.segments.isEmpty()) {
                    _state.value = _state.value.copy(
                        playing = false, error = "该章没有可播放的音频片段",
                    )
                    return@launch
                }
                _state.value = _state.value.copy(total = m.segments.size)
                val out = sinkFactory.create()
                sink = out
                var started = false
                for (seg in m.segments.sortedBy { it.index }) {
                    if (!isActive()) return@launch
                    if (seg.wrappedDek.isEmpty()) {
                        _state.value = _state.value.copy(
                            playing = false, error = "服务端未返回设备可解的音频密钥",
                        )
                        return@launch
                    }
                    val audioKey = keyStore.unwrapDEK(seg.wrappedDek)
                    val raw = api.segmentBytes(seg.key)
                    val nonce = seg.audioNonce.decodeBase64Bytes()
                    val end = seg.ctOffset + seg.ctLen
                    if (seg.ctOffset < 0 || end > raw.size) {
                        zeroBytes(audioKey)
                        throw IllegalStateException("音频片段长度异常")
                    }
                    val ct = raw.copyOfRange(seg.ctOffset, end)
                    val aad = "novelstack-tts-audio-v1|${seg.key}".encodeToByteArray()
                    val wav = AesGcm.open(audioKey, nonce, ct, aad)
                    val pcm = parseWav(wav)
                    if (!started) {
                        out.start(pcm.sampleRate, pcm.channels)
                        out.setSpeed(s)
                        started = true
                    }
                    out.write(pcm.pcm)
                    _state.value = _state.value.copy(segment = seg.index + 1)
                    zeroBytes(audioKey)
                    zeroBytes(ct)
                    zeroBytes(wav)
                }
                _state.value = _state.value.copy(playing = false, paused = false)
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Throwable) {
                _state.value = _state.value.copy(
                    playing = false, error = e.message ?: "播放失败",
                )
            } finally {
                sink?.stop()
                sink = null
            }
        }
    }

    fun pause() {
        if (!_state.value.playing) return
        sink?.pause()
        _state.value = _state.value.copy(paused = true)
    }

    fun resume() {
        if (!_state.value.playing) return
        sink?.resume()
        _state.value = _state.value.copy(paused = false)
    }

    fun stop() {
        job?.cancel()
        job = null
        sink?.stop()
        sink = null
        _state.value = _state.value.copy(playing = false, paused = false, segment = 0, total = 0)
    }

    fun setSpeed(speed: Float) {
        val s = speed.coerceIn(0.5f, 2.0f)
        sink?.setSpeed(s)
        _state.value = _state.value.copy(speed = s)
    }

    private fun isActive(): Boolean = job?.isActive == true
}
