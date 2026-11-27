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
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch

/**
 * TTS 播放控制器（第七阶段）。
 *
 * 数据流（全部在内存中，绝不落盘）：
 *   服务端 NSAC 密文 ──RSA 解包音频 DEK──> AES-GCM 解密 ──> WAV 片段流读取器
 *   ──剥离每个片段的 RIFF 头、拼接连续 PCM──> AudioTrack
 *
 * 职责：
 *  - 驱动 [PlaybackStateMachine] 做状态流转；
 *  - 用 [WavSegmentStream] 顺序消费解密后的 WAV 片段（HTTP chunk 与片段边界可不对齐）；
 *  - 断点续听：暂停时记住当前片段序号，恢复时从该片段继续；
 *  - 定时关闭：[SleepTimer] 到点或「播完当前章」时优雅停止并发 [PlaybackSignal.FinishForeground]；
 *  - 音频焦点/耳机拔出：由前台服务调用 [onAudioFocusLoss] / [onBecomingNoisy] 暂停。
 *
 * 安全： decrypted key/ct/wav/pcm 只用完即清零；State 里只放元数据与计数，不含音频或正文。
 */
class TtsController(
    private val api: NovelApi,
    private val keyStore: DeviceKeyStore,
    private val sinkFactory: AudioSinkFactory,
    private val scope: CoroutineScope,
) {
    data class State(
        val status: PlaybackStatus = PlaybackStatus.IDLE,
        val playing: Boolean = false,
        val paused: Boolean = false,
        val bookId: String = "",
        val chapterIndex: Int = -1,
        val segment: Int = 0,
        val total: Int = 0,
        val speed: Float = 1.0f,
        val sleepMode: SleepMode = SleepMode.OFF,
        val sleepRemainingMs: Long = 0L,
        val bookTitle: String = "",
        val chapterTitle: String = "",
        val error: String? = null,
    )

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> = _state

    private val _signals = MutableSharedFlow<PlaybackSignal>(extraBufferCapacity = 4)
    val signals: SharedFlow<PlaybackSignal> = _signals.asSharedFlow()

    private val machine = PlaybackStateMachine()
    private val sleepTimer = SleepTimer()

    private var job: Job? = null
    private var sleepJob: Job? = null
    private var sink: AudioSink? = null

    // 断点续听：暂停时记住「下一个待播放片段」的序号（0-based）。
    private var resumeSegment = 0

    // 通知栏元数据提供者：由 AppState 注入，只返回书名/章节标题。
    private var metadataProvider: (() -> PlaybackMetadata)? = null

    // 章节导航提供者：由 AppState 注入，返回目标章节序号（越界返回 null）。
    private var chapterNav: ChapterNavigation? = null

    /** 章节导航：根据当前序号解析上一章/下一章；无可用章节返回 null。 */
    interface ChapterNavigation {
        fun next(current: Int): Int?
        fun prev(current: Int): Int?
    }

    fun setMetadataProvider(provider: () -> PlaybackMetadata) {
        metadataProvider = provider
    }

    fun setChapterNavigation(nav: ChapterNavigation) {
        chapterNav = nav
    }

    /** 从当前阅读章节开始朗读（供通知栏「播放」按钮使用）。 */
    fun playCurrent(bookId: String, chapterIndex: Int) {
        play(bookId, chapterIndex, _state.value.speed)
    }

    fun nextChapter() {
        val st = _state.value
        val target = chapterNav?.next(st.chapterIndex) ?: return
        machine.send(PlaybackEvent.NextChapter(target))
        play(st.bookId, target, st.speed)
    }

    fun prevChapter() {
        val st = _state.value
        val target = chapterNav?.prev(st.chapterIndex) ?: return
        machine.send(PlaybackEvent.PrevChapter(target))
        play(st.bookId, target, st.speed)
    }

    /** 开始（或从断点续听）一章的朗读。 */
    fun play(bookId: String, chapterIndex: Int, speed: Float, fromSegment: Int = 0) {
        stopInternal(emitFinish = false)
        resumeSegment = fromSegment.coerceAtLeast(0)
        val meta = runCatching { metadataProvider?.invoke() }.getOrNull() ?: PlaybackMetadata()
        val s = speed.coerceIn(0.5f, 2.0f)
        _state.value = State(
            status = PlaybackStatus.PREPARING, playing = true,
            bookId = bookId, chapterIndex = chapterIndex, speed = s,
            sleepMode = sleepTimer.mode(), bookTitle = meta.bookTitle, chapterTitle = meta.chapterTitle,
        )
        machine.send(PlaybackEvent.Play(bookId, chapterIndex))

        job = scope.launch(Dispatchers.Default) {
            // 第九阶段：首帧耗时 = 点击播放 → 第一块 PCM 写入 AudioTrack。
            // 只记时间，不含任何文本或音频内容。
            val firstFrameStart = com.novelstack.reader.telemetry.Telemetry.now()
            try {
                api.prepareAudio(bookId, chapterIndex, s)
                val m: AudioManifest = api.audioManifest(bookId, chapterIndex)
                if (m.segments.isEmpty()) {
                    fail("该章没有可播放的音频片段")
                    return@launch
                }
                val segments = m.segments.sortedBy { it.index }
                _state.value = _state.value.copy(total = segments.size)
                val out = sinkFactory.create()
                sink = out
                val stream = WavSegmentStream()
                var started = false

                for (seg in segments) {
                    if (!isActive) return@launch
                    if (seg.index < resumeSegment) continue // 断点：跳过已听过的片段
                    if (seg.wrappedDek.isEmpty()) {
                        fail("服务端未返回设备可解的音频密钥")
                        return@launch
                    }

                    val audioKey = keyStore.unwrapDEK(seg.wrappedDek)
                    val raw = api.segmentBytes(seg.key)
                    val nonce = seg.audioNonce.decodeBase64Bytes()
                    val end = seg.ctOffset + seg.ctLen
                    if (seg.ctOffset < 0 || end > raw.size) {
                        zeroBytes(audioKey)
                        fail("音频片段长度异常")
                        return@launch
                    }
                    val ct = raw.copyOfRange(seg.ctOffset, end)
                    val aad = "novelstack-tts-audio-v1|${seg.key}".encodeToByteArray()
                    val wav = AesGcm.open(audioKey, nonce, ct, aad)
                    val blocks = try {
                        stream.feed(wav)
                        stream.takePcm()
                    } finally {
                        zeroBytes(audioKey); zeroBytes(ct); zeroBytes(wav)
                    }

                    for (block in blocks) {
                        if (!isActive) return@launch
                        if (!started) {
                            out.start(block.sampleRate, block.channels)
                            out.setSpeed(s)
                            started = true
                            com.novelstack.reader.telemetry.Telemetry.record(
                                kind = com.novelstack.reader.telemetry.TelemetryKind.TTS_FIRST_FRAME,
                                name = "tts_first_frame",
                                durationMs = com.novelstack.reader.telemetry.Telemetry.now() - firstFrameStart,
                                context = mapOf(
                                    "speed" to s.toString(),
                                    "resume_segment" to resumeSegment.toString(),
                                ),
                            )
                        }
                        out.write(block.pcm)
                        zeroBytes(block.pcm)
                    }
                    resumeSegment = seg.index + 1
                    machine.send(PlaybackEvent.SegmentAdvanced(resumeSegment))
                    _state.value = _state.value.copy(segment = resumeSegment)
                }

                // 整章播完：播完当前章模式在此触发定时停止。
                sleepTimer.onChapterEnded()
                finishNaturally()
            } catch (e: kotlinx.coroutines.CancellationException) {
                throw e
            } catch (e: Throwable) {
                fail(e.message ?: "播放失败")
            } finally {
                sink?.stop()
                sink = null
            }
        }
    }

    fun pause() {
        if (!_state.value.playing || _state.value.paused) return
        sink?.pause()
        machine.send(PlaybackEvent.Pause)
        _state.value = _state.value.copy(
            status = PlaybackStatus.PAUSED, paused = true,
        )
    }

    fun resume() {
        if (!_state.value.playing || !_state.value.paused) return
        sink?.resume()
        machine.send(PlaybackEvent.Resume)
        _state.value = _state.value.copy(
            status = PlaybackStatus.PLAYING, paused = false,
        )
    }

    fun stop() = stopInternal(emitFinish = true)

    /** 音频焦点丢失 / 耳机拔出：与手动暂停等价，自动暂停。 */
    fun onAudioFocusLoss() {
        if (_state.value.playing && !_state.value.paused) {
            sink?.pause()
            machine.send(PlaybackEvent.AudioFocusLoss)
            _state.value = _state.value.copy(status = PlaybackStatus.PAUSED, paused = true)
        }
    }

    fun onAudioFocusGain() {
        if (_state.value.playing && _state.value.paused) {
            sink?.resume()
            machine.send(PlaybackEvent.AudioFocusGain)
            _state.value = _state.value.copy(status = PlaybackStatus.PLAYING, paused = false)
        }
    }

    fun onBecomingNoisy() = onAudioFocusLoss()

    fun setSpeed(speed: Float) {
        val s = speed.coerceIn(0.5f, 2.0f)
        sink?.setSpeed(s)
        _state.value = _state.value.copy(speed = s)
    }

    /** 设置定时关闭；播放中即刻启动倒计时协程。 */
    fun setSleepMode(mode: SleepMode) {
        sleepTimer.start(mode)
        _state.value = _state.value.copy(
            sleepMode = mode,
            sleepRemainingMs = sleepTimer.remainingMs(),
        )
        sleepJob?.cancel()
        if (mode != SleepMode.OFF && mode != SleepMode.END_OF_CHAPTER && _state.value.playing) {
            startSleepTicker()
        }
    }

    private fun startSleepTicker() {
        sleepJob = scope.launch(Dispatchers.Default) {
            while (isActive && _state.value.playing) {
                if (sleepTimer.tick()) {
                    finishNaturally()
                    return@launch
                }
                _state.value = _state.value.copy(sleepRemainingMs = sleepTimer.remainingMs())
                delay(1000)
            }
        }
    }

    private fun finishNaturally() {
        // 到点/播完都走状态机的 Stop，保证状态一致。
        machine.send(PlaybackEvent.Stop)
        _state.value = _state.value.copy(
            status = PlaybackStatus.STOPPED, playing = false, paused = false,
            sleepRemainingMs = 0L,
        )
        stopInternal(emitFinish = false)
        _signals.tryEmit(PlaybackSignal.FinishForeground)
    }

    private fun fail(message: String) {
        machine.send(PlaybackEvent.Failed(message))
        _state.value = _state.value.copy(
            status = PlaybackStatus.ERROR, playing = false, paused = false, error = message,
        )
        _signals.tryEmit(PlaybackSignal.FinishForeground)
    }

    private fun stopInternal(emitFinish: Boolean) {
        job?.cancel()
        job = null
        sleepJob?.cancel()
        sleepJob = null
        sink?.stop()
        sink = null
        machine.send(PlaybackEvent.Stop)
        sleepTimer.cancel()
        _state.value = State(
            speed = _state.value.speed,
            status = PlaybackStatus.STOPPED,
        )
        if (emitFinish) _signals.tryEmit(PlaybackSignal.FinishForeground)
    }

    private val isActive: Boolean get() = job?.isActive == true
}
