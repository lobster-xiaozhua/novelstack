package com.novelstack.reader.tts

/**
 * 播放状态机（纯逻辑）：播放 / 暂停 / 切章时的状态流转集中在这里，
 * 便于用单元测试覆盖，避免把 if/else 散落在 Service 与 UI 之间。
 *
 * 它不持有音频、不做网络调用，只根据 [PlaybackEvent] 决定下一个 [PlaybackStatus]。
 * 上层（TtsController / PlaybackService）据此驱动 AudioTrack 与前台服务。
 */
class PlaybackStateMachine(initial: PlaybackStatus = PlaybackStatus.IDLE) {

    var state: PlaybackStatus = initial
        private set

    /** 应用一个事件，返回（也可能相同的）新状态。 */
    fun send(event: PlaybackEvent): PlaybackStatus {
        val next = when (event) {
            is PlaybackEvent.Play,
            is PlaybackEvent.NextChapter,
            is PlaybackEvent.PrevChapter,
            -> PlaybackStatus.PREPARING

            PlaybackEvent.Prepared -> PlaybackStatus.PLAYING
            PlaybackEvent.Started -> PlaybackStatus.PLAYING
            PlaybackEvent.Resume, PlaybackEvent.AudioFocusGain -> if (state == PlaybackStatus.PAUSED) {
                PlaybackStatus.PLAYING
            } else {
                state
            }
            PlaybackEvent.Pause,
            PlaybackEvent.AudioFocusLoss,
            PlaybackEvent.BecomingNoisy,
            -> if (state == PlaybackStatus.PLAYING || state == PlaybackStatus.PREPARING) {
                PlaybackStatus.PAUSED
            } else {
                state
            }
            PlaybackEvent.Stop, PlaybackEvent.ChapterComplete -> PlaybackStatus.STOPPED
            is PlaybackEvent.Failed -> PlaybackStatus.ERROR
            is PlaybackEvent.SegmentAdvanced -> state // 片段推进不改变状态
        }
        state = next
        return next
    }

    fun isActive(): Boolean =
        state == PlaybackStatus.PLAYING || state == PlaybackStatus.PAUSED || state == PlaybackStatus.PREPARING
}
