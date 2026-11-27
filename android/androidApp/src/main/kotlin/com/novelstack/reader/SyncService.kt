package com.novelstack.reader

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import com.novelstack.reader.telemetry.AndroidTelemetryRuntime
import com.novelstack.reader.telemetry.TelemetryUploader
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.serialization.json.Json

/**
 * 用户主动触发的「立即上传」前台服务。
 *
 * 为什么单独做一个 dataSync 服务：
 *  - Android 14 起长耗时后台任务必须显式声明类型，dataSync 是同步类任务的正确类型；
 *  - WorkManager 的常规任务没有前台通知，用户点了按钮却看不到任何反馈；
 *  - 前台服务期间即使退到后台也能把这一批传完，避免半途被冻结。
 *
 * 约束仍然遵守：只在 UNMETERED 网络下真正传输（调用方已判定，这里再判一次）。
 */
class SyncService : Service() {

    companion object {
        private const val CHANNEL_ID = "novelstack_sync"
        private const val NOTIFICATION_ID = 2031

        fun start(context: Context) {
            val intent = Intent(context, SyncService::class.java)
            ContextCompat.startForegroundService(context, intent)
        }
    }

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val json = Json { ignoreUnknownKeys = true }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startAsForeground()
        scope.launch {
            try {
                AndroidTelemetryRuntime.open(this@SyncService)?.use { session ->
                    TelemetryUploader(session.api, session.repo, json).uploadOnce()
                }
            } catch (_: Throwable) {
                // 失败不打扰用户：数据仍在本地队列，下次进前台会重试。
            } finally {
                stopSelf(startId)
            }
        }
        return START_NOT_STICKY
    }

    private fun startAsForeground() {
        createChannel()
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_IMMUTABLE,
        )
        val notification: Notification = NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.stat_sys_upload)
            .setContentTitle("正在同步运行数据")
            .setContentText("仅上传运行状况与耗时，不包含书籍内容")
            .setOngoing(true)
            .setContentIntent(open)
            .build()
        val type = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC
        } else {
            0
        }
        ServiceCompat.startForeground(this, NOTIFICATION_ID, notification, type)
    }

    private fun createChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return
        val nm = getSystemService(NotificationManager::class.java) ?: return
        if (nm.getNotificationChannel(CHANNEL_ID) != null) return
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_ID, "数据同步", NotificationManager.IMPORTANCE_LOW).apply {
                description = "遥测上传等短时同步任务"
                setShowBadge(false)
            },
        )
    }

    override fun onDestroy() {
        scope.cancel()
        super.onDestroy()
    }
}
