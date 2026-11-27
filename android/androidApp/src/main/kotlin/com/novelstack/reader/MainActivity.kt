package com.novelstack.reader

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.core.content.ContextCompat
import com.novelstack.reader.playback.PlaybackService
import com.novelstack.reader.ui.NovelApp

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val state = (application as ReaderApplication).state

        // 开始朗读时先拉起前台服务（保证切后台/锁屏不被杀）；服务内部再控制播放。
        state.onPlaybackStarted = { PlaybackService.start(this) }

        // 第八阶段：阅读页左右侧上下滑动调节系统亮度（作用于本 Activity 窗口）。
        state.onBrightnessChange = { value ->
            window.attributes = window.attributes.apply { screenBrightness = value }
        }

        // Android 13+ 通知权限：媒体通知需要用户授权。
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
            ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) !=
            PackageManager.PERMISSION_GRANTED
        ) {
            requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 2027)
        }

        setContent { NovelApp(state) }
    }

    // 第七阶段：不在 onStop 停止朗读。播放由前台服务承载，切后台/锁屏时继续。
}
