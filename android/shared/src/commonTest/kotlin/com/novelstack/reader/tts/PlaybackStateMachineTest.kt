package com.novelstack.reader.tts

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** 状态机：播放 / 暂停 / 切章 / 音频焦点中断的状态流转。 */
class PlaybackStateMachineTest {

    @Test
    fun fullPlayReferenceFlow() {
        val sm = PlaybackStateMachine()
        assertEquals(PlaybackStatus.IDLE, sm.state)
        sm.send(PlaybackEvent.Play("b", 0))
        assertEquals(PlaybackStatus.PREPARING, sm.state)
        sm.send(PlaybackEvent.Prepared)
        assertEquals(PlaybackStatus.PLAYING, sm.state)
        sm.send(PlaybackEvent.SegmentAdvanced(3))
        assertEquals(PlaybackStatus.PLAYING, sm.state)
        sm.send(PlaybackEvent.Pause)
        assertEquals(PlaybackStatus.PAUSED, sm.state)
        sm.send(PlaybackEvent.Resume)
        assertEquals(PlaybackStatus.PLAYING, sm.state)
        sm.send(PlaybackEvent.ChapterComplete)
        assertEquals(PlaybackStatus.STOPPED, sm.state)
    }

    @Test
    fun nextAndPrevChapterReprepare() {
        val sm = PlaybackStateMachine()
        sm.send(PlaybackEvent.Play("b", 0))
        sm.send(PlaybackEvent.Prepared)
        assertEquals(PlaybackStatus.PLAYING, sm.state)

        sm.send(PlaybackEvent.NextChapter(1))
        assertEquals(PlaybackStatus.PREPARING, sm.state)
        sm.send(PlaybackEvent.Prepared)
        assertEquals(PlaybackStatus.PLAYING, sm.state)

        sm.send(PlaybackEvent.PrevChapter(0))
        assertEquals(PlaybackStatus.PREPARING, sm.state)
    }

    @Test
    fun audioInterruptionsPauseAndResume() {
        val sm = PlaybackStateMachine()
        sm.send(PlaybackEvent.Play("b", 0))
        sm.send(PlaybackEvent.Prepared)

        sm.send(PlaybackEvent.AudioFocusLoss)
        assertEquals(PlaybackStatus.PAUSED, sm.state)
        sm.send(PlaybackEvent.AudioFocusGain)
        assertEquals(PlaybackStatus.PLAYING, sm.state)

        sm.send(PlaybackEvent.BecomingNoisy)
        assertEquals(PlaybackStatus.PAUSED, sm.state)
    }

    @Test
    fun failureLeadsToErrorAndCanRestart() {
        val sm = PlaybackStateMachine()
        sm.send(PlaybackEvent.Play("b", 0))
        sm.send(PlaybackEvent.Failed("解密失败"))
        assertEquals(PlaybackStatus.ERROR, sm.state)
        sm.send(PlaybackEvent.Play("b", 0))
        assertEquals(PlaybackStatus.PREPARING, sm.state)
    }

    @Test
    fun resumeIgnoredWhenNotPaused() {
        val sm = PlaybackStateMachine()
        sm.send(PlaybackEvent.Play("b", 0))
        sm.send(PlaybackEvent.Prepared)
        sm.send(PlaybackEvent.Resume) // PLAYING 下 resume 不改变状态
        assertEquals(PlaybackStatus.PLAYING, sm.state)
        assertFalse(sm.isActive() == false)
        sm.send(PlaybackEvent.Stop)
        assertFalse(sm.isActive())
    }
}
