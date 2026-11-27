package com.novelstack.reader

import android.Manifest
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.os.Process
import android.os.SystemClock
import android.view.Choreographer
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.core.app.ActivityCompat
import androidx.core.content.ContextCompat
import com.journeyapps.barcodescanner.ScanContract
import com.journeyapps.barcodescanner.ScanOptions
import com.novelstack.reader.playback.PlaybackService
import com.novelstack.reader.telemetry.Telemetry
import com.novelstack.reader.telemetry.TelemetryKind
import com.novelstack.reader.ui.NovelApp

class MainActivity : ComponentActivity() {

    private var jankCallback: Choreographer.FrameCallback? = null
    /** 当前等待扫码结果的回调（扫码是一次性动作）。 */
    private var pendingQrResult: ((String) -> Unit)? = null

    private val qrLauncher = registerForActivityResult(ScanContract()) { result ->
        val content = result.contents
        if (content != null) {
            pendingQrResult?.invoke(content)
        }
        pendingQrResult = null
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // Android 15 起强制 Edge-to-Edge；显式开启保证 13/14 也一致，Insets 由 Compose 处理。
        enableEdgeToEdge()
        val state = (application as ReaderApplication).state

        // 开始朗读时先拉起前台服务（保证切后台/锁屏不被杀）；服务内部再控制播放。
        state.onPlaybackStarted = { PlaybackService.start(this) }

        // 第八阶段：阅读页左右侧上下滑动调节系统亮度（作用于本 Activity 窗口）。
        state.onBrightnessChange = { value ->
            window.attributes = window.attributes.apply { screenBrightness = value }
        }

        // 第十阶段：扫描控制台配对二维码（ZXing，纯本地解码）。
        state.onScanQr = { onResult ->
            pendingQrResult = onResult
            val options = ScanOptions()
                .setDesiredBarcodeFormats(ScanOptions.QR_CODE)
                .setPrompt("扫描控制台生成的服务端配对二维码")
                .setBeepEnabled(false)
            qrLauncher.launch(options)
        }

        // Android 13+ 通知权限：媒体通知与同步通知都需要用户授权。
        requestNotificationPermissionIfNeeded()

        recordColdStartOnce()
        installJankMonitor()

        setContent { NovelApp(state) }
    }

    override fun onResume() {
        super.onResume()
        // 每次回前台触发一次受约束的遥测上传（仅 UNMETERED 真正传输）。
        Telemetry.requestUpload()
    }

    override fun onDestroy() {
        jankCallback?.let { Choreographer.getInstance().removeFrameCallback(it) }
        jankCallback = null
        super.onDestroy()
    }

    private fun requestNotificationPermissionIfNeeded() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.TIRAMISU) return
        val granted = ContextCompat.checkSelfPermission(this, Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED
        if (granted) return
        // 先解释再申请：拒绝后 TTS 前台服务仍能跑，但通知不可见。
        ActivityCompat.requestPermissions(this, arrayOf(Manifest.permission.POST_NOTIFICATIONS), 2027)
    }

    /** 冷启动：进程启动 → 首帧绘制，只记一次。 */
    private fun recordColdStartOnce() {
        val processStart = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.N) {
            Process.getStartUptimeMillis()
        } else {
            SystemClock.uptimeMillis()
        }
        window.decorView.viewTreeObserver.addOnPreDrawListener(
            object : android.view.ViewTreeObserver.OnPreDrawListener {
                override fun onPreDraw(): Boolean {
                    window.decorView.viewTreeObserver.removeOnPreDrawListener(this)
                    val cost = SystemClock.uptimeMillis() - processStart
                    Telemetry.record(
                        kind = TelemetryKind.COLD_START,
                        name = "first_frame",
                        durationMs = cost,
                        context = mapOf("since" to "process_start"),
                    )
                    return true
                }
            },
        )
    }

    /**
     * 卡顿检测：连续 3 帧超过 32ms（约 2 个 vsync）判定一次 JANK。
     * 只记录连续段里最差的一帧，避免一卡就刷满队列。
     */
    private fun installJankMonitor() {
        val choreographer = Choreographer.getInstance()
        var lastFrame = 0L
        var consecutive = 0
        var worst = 0L
        val cb = object : Choreographer.FrameCallback {
            override fun doFrame(frameTimeNanos: Long) {
                if (lastFrame != 0L) {
                    val deltaMs = (frameTimeNanos - lastFrame) / 1_000_000
                    if (deltaMs > 32) {
                        consecutive++
                        if (deltaMs > worst) worst = deltaMs
                        if (consecutive >= 3) {
                            Telemetry.record(
                                kind = TelemetryKind.JANK,
                                name = "consecutive_slow_frames",
                                durationMs = worst,
                                context = mapOf("frames" to consecutive.toString()),
                            )
                            consecutive = 0
                            worst = 0
                        }
                    } else {
                        consecutive = 0
                        worst = 0
                    }
                }
                lastFrame = frameTimeNanos
                choreographer.postFrameCallback(this)
            }
        }
        jankCallback = cb
        choreographer.postFrameCallback(cb)
    }
}
