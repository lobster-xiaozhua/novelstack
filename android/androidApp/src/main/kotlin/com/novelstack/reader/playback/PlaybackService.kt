package com.novelstack.reader.playback

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.media.AudioManager
import android.media.AudioFocusRequest
import android.media.session.MediaSession
import android.media.session.PlaybackState
import android.os.Build
import android.os.IBinder
import android.support.v4.media.MediaMetadataCompat
import android.support.v4.media.session.MediaSessionCompat
import android.support.v4.media.session.PlaybackStateCompat
import androidx.core.app.NotificationCompat
import androidx.media.app.NotificationCompat.MediaStyle
import com.novelstack.reader.MainActivity
import com.novelstack.reader.ReaderApplication
import com.novelstack.reader.tts.PlaybackMetadata
import com.novelstack.reader.tts.PlaybackSignal
import com.novelstack.reader.tts.PlaybackStatus
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.collectLatest
import kotlinx.coroutines.launch

/**
 * 第七阶段：承载朗读的前台服务。
 *
 * - Android 14 (API 34) 起媒体前台服务必须声明 type=mediaPlayback，
 *   且 startForeground 必须传 FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK，否则抛
 *   SecurityException 崩溃。Manifest 中已声明 foregroundServiceType 与对应权限。
 * - MediaSessionCompat 接管通知栏/锁屏控制（播放、暂停、上一章、下一章）。
 * - 通知内容与 metadata 只放书名/章节标题，绝不放正文片段。
 * - 音频焦点与 ACTION_AUDIO_BECOMING_NOISY：中断时暂停，恢复时继续。
 *
 * 音频数据从不经过本服务落盘：解码与播放全部在 TtsController + AudioTrack 内存中完成。
 */
class PlaybackService : Service() {

    companion object {
        private const val CHANNEL_ID = "novelstack_tts"
        private const val NOTIFICATION_ID = 2027
        const val ACTION_PLAY = "com.novelstack.reader.playback.PLAY"
        const val ACTION_PAUSE = "com.novelstack.reader.playback.PAUSE"
        const val ACTION_STOP = "com.novelstack.reader.playback.STOP"
        const val ACTION_NEXT = "com.novelstack.reader.playback.NEXT"
        const val ACTION_PREV = "com.novelstack.reader.playback.PREV"

        fun start(context: Context) {
            val intent = Intent(context, PlaybackService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(intent)
            } else {
                context.startService(intent)
            }
        }
    }

    private val scope = CoroutineScope(Dispatchers.Main + Job())
    private lateinit var mediaSession: MediaSessionCompat
    private var noisyReceiver: NoisyReceiver? = null
    private var audioFocusRequested = false

    private val audioFocusListener = AudioManager.OnAudioFocusChangeListener { focusChange ->
        when (focusChange) {
            AudioManager.AUDIOFOCUS_LOSS,
            AudioManager.AUDIOFOCUS_LOSS_TRANSIENT,
            AudioManager.AUDIOFOCUS_LOSS_TRANSIENT_CAN_DUCK,
            -> tts().onAudioFocusLoss()
            AudioManager.AUDIOFOCUS_GAIN -> tts().onAudioFocusGain()
        }
    }

    private fun app() = application as ReaderApplication
    private fun tts() = app().state.tts

    override fun onCreate() {
        super.onCreate()
        createChannel()
        mediaSession = MediaSessionCompat(this, "NovelStackTTS").apply {
            setCallback(object : MediaSessionCompat.Callback() {
                override fun onPlay() = handlePlay()
                override fun onPause() = tts().pause()
                override fun onStop() = tts().stop()
                override fun onSkipToNext() = tts().nextChapter()
                override fun onSkipToPrevious() = tts().prevChapter()
                override fun onSetPlaybackSpeed(speed: Float) = tts().setSpeed(speed)
            })
            // 用 Activity 的 PendingIntent 作为会话入口，锁屏点击回到应用。
            val pi = PendingIntent.getActivity(
                this@PlaybackService, 0,
                Intent(this@PlaybackService, MainActivity::class.java),
                PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
            )
            setSessionActivity(pi)
            isActive = true
        }

        observeState()
        observeSignals()
        registerNoisyReceiver()
    }

    private fun observeState() {
        scope.launch {
            tts().state.collectLatest { st ->
                updateMediaSession(st.status, st.speed, st.bookTitle, st.chapterTitle)
                if (st.status == PlaybackStatus.PAUSED || st.status == PlaybackStatus.STOPPED ||
                    st.status == PlaybackStatus.IDLE || st.status == PlaybackStatus.ERROR
                ) {
                    // 暂停/停止时释放音频焦点，避免长期占用。
                    abandonAudioFocus()
                }
                val notification = buildNotification(st.bookTitle, st.chapterTitle, st.status)
                if (st.status == PlaybackStatus.IDLE || st.status == PlaybackStatus.STOPPED) {
                    stopForeground(STOP_FOREGROUND_REMOVE)
                } else {
                    startForeground(NOTIFICATION_ID, notification)
                }
            }
        }
    }

    private fun observeSignals() {
        scope.launch {
            tts().signals.collectLatest { signal ->
                when (signal) {
                    PlaybackSignal.FinishForeground -> {
                        stopForeground(STOP_FOREGROUND_REMOVE)
                        stopSelf()
                    }
                }
            }
        }
    }

    private fun handlePlay() {
        requestAudioFocus()
        val st = tts().state.value
        if (st.status == PlaybackStatus.PAUSED) {
            tts().resume()
        } else if (!st.playing) {
            val reader = app().state.reader.value
            val bookId = reader.book?.id
            if (bookId != null) tts().playCurrent(bookId, reader.index)
        }
    }

    private fun updateMediaSession(
        status: PlaybackStatus,
        speed: Float,
        bookTitle: String,
        chapterTitle: String,
    ) {
        // metadata 只含书名/章节标题，不含正文。
        val metadata = MediaMetadataCompat.Builder()
            .putString(MediaMetadataCompat.METADATA_KEY_TITLE, chapterTitle)
            .putString(MediaMetadataCompat.METADATA_KEY_ALBUM, bookTitle)
            .putString(MediaMetadataCompat.METADATA_KEY_ARTIST, "NovelStack")
            .build()
        mediaSession.setMetadata(metadata)

        val pbState = when (status) {
            PlaybackStatus.PLAYING -> PlaybackStateCompat.STATE_PLAYING
            PlaybackStatus.PAUSED -> PlaybackStateCompat.STATE_PAUSED
            PlaybackStatus.PREPARING -> PlaybackStateCompat.STATE_BUFFERING
            PlaybackStatus.STOPPED, PlaybackStatus.IDLE -> PlaybackStateCompat.STATE_STOPPED
            PlaybackStatus.ERROR -> PlaybackStateCompat.STATE_ERROR
        }
        val actions = PlaybackStateCompat.ACTION_PLAY or
            PlaybackStateCompat.ACTION_PAUSE or
            PlaybackStateCompat.ACTION_STOP or
            PlaybackStateCompat.ACTION_SKIP_TO_NEXT or
            PlaybackStateCompat.ACTION_SKIP_TO_PREVIOUS or
            PlaybackStateCompat.ACTION_SET_PLAYBACK_SPEED
        mediaSession.setPlaybackState(
            PlaybackStateCompat.Builder()
                .setState(pbState, PlaybackState.PLAYBACK_POSITION_UNKNOWN, speed, System.currentTimeMillis())
                .setActions(actions)
                .build(),
        )
    }

    private fun buildNotification(bookTitle: String, chapterTitle: String, status: PlaybackStatus): Notification {
        val controller = mediaSession.controller
        val sessionToken = mediaSession.sessionToken

        val playPauseIcon = if (status == PlaybackStatus.PLAYING) {
            android.R.drawable.ic_media_pause
        } else {
            android.R.drawable.ic_media_play
        }
        val playPauseIntent = actionIntent(if (status == PlaybackStatus.PLAYING) ACTION_PAUSE else ACTION_PLAY)

        return NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.ic_media_play)
            .setContentTitle(chapterTitle.ifBlank { "朗读" })
            .setContentText(bookTitle)
            .setOnlyAlertOnce(true)
            .setOngoing(status == PlaybackStatus.PLAYING || status == PlaybackStatus.PREPARING)
            .addAction(playPauseIcon, if (status == PlaybackStatus.PLAYING) "暂停" else "播放", playPauseIntent)
            .addAction(android.R.drawable.ic_media_previous, "上一章", actionIntent(ACTION_PREV))
            .addAction(android.R.drawable.ic_media_next, "下一章", actionIntent(ACTION_NEXT))
            .setStyle(
                MediaStyle()
                    .setMediaSession(sessionToken)
                    .setShowActionsInCompactView(0, 1, 2),
            )
            .setContentIntent(controller.sessionActivity)
            .build()
    }

    private fun actionIntent(action: String): PendingIntent {
        val intent = Intent(this, PlaybackService::class.java).setAction(action)
        return PendingIntent.getService(
            this, action.hashCode(), intent,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
    }

    private fun createChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val channel = NotificationChannel(
                CHANNEL_ID, "朗读播放控制", NotificationManager.IMPORTANCE_LOW,
            ).apply { description = "后台朗读的播放/暂停与章节控制" }
            getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
        }
    }

    private fun requestAudioFocus() {
        if (audioFocusRequested) return
        val am = getSystemService(Context.AUDIO_SERVICE) as AudioManager
        val result = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            am.requestAudioFocus(
                AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN)
                    .setAudioAttributes(
                        android.media.AudioAttributes.Builder()
                            .setUsage(android.media.AudioAttributes.USAGE_MEDIA)
                            .setContentType(android.media.AudioAttributes.CONTENT_TYPE_SPEECH)
                            .build(),
                    )
                    .setOnAudioFocusChangeListener(audioFocusListener)
                    .build(),
            )
        } else {
            @Suppress("DEPRECATION")
            am.requestAudioFocus(
                audioFocusListener, AudioManager.STREAM_MUSIC, AudioManager.AUDIOFOCUS_GAIN,
            )
        }
        audioFocusRequested = result == AudioManager.AUDIOFOCUS_REQUEST_GRANTED
    }

    private fun abandonAudioFocus() {
        if (!audioFocusRequested) return
        val am = getSystemService(Context.AUDIO_SERVICE) as AudioManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            am.abandonAudioFocusRequest(
                AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN)
                    .setOnAudioFocusChangeListener(audioFocusListener)
                    .build(),
            )
        } else {
            @Suppress("DEPRECATION")
            am.abandonAudioFocus(audioFocusListener)
        }
        audioFocusRequested = false
    }

    private fun registerNoisyReceiver() {
        noisyReceiver = NoisyReceiver().also { receiver ->
            registerReceiver(receiver, IntentFilter(AudioManager.ACTION_AUDIO_BECOMING_NOISY))
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_PLAY -> handlePlay()
            ACTION_PAUSE -> tts().pause()
            ACTION_STOP -> tts().stop()
            ACTION_NEXT -> tts().nextChapter()
            ACTION_PREV -> tts().prevChapter()
        }
        // 前台服务被系统回收后应重建，但不重放 intent（避免暂停后重启又播放）。
        return START_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onDestroy() {
        noisyReceiver?.let { runCatching { unregisterReceiver(it) } }
        noisyReceiver = null
        abandonAudioFocus()
        mediaSession.isActive = false
        mediaSession.release()
        scope.cancel()
        super.onDestroy()
    }

    /** 耳机拔出 → 自动暂停（等价于音频焦点丢失）。 */
    private inner class NoisyReceiver : android.content.BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            if (intent?.action == AudioManager.ACTION_AUDIO_BECOMING_NOISY) {
                tts().onBecomingNoisy()
            }
        }
    }
}
