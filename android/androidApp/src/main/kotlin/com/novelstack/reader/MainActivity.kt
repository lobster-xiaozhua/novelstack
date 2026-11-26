package com.novelstack.reader

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import com.novelstack.reader.ui.NovelApp

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val state = (application as ReaderApplication).state
        setContent { NovelApp(state) }
    }

    override fun onStop() {
        super.onStop()
        // 退到后台时停止朗读，避免持有 AudioTrack。
        (application as ReaderApplication).state.tts.stop()
    }
}
