package com.novelstack.reader.telemetry

import android.content.Context
import android.os.Build
import com.novelstack.reader.shared.BuildConfig

/**
 * Android 侧 Sink：同步写 SQLCipher → 请求受约束的上传。
 *
 * 注意 [insertSync] 直接在调用线程执行 SQL：崩溃处理线程里不会有协程调度，
 * 这正是「进程被杀也不丢」的关键。数据库写入通常在 1ms 量级，可接受。
 */
class AndroidTelemetrySink(
    private val context: Context,
    private val repo: TelemetryRepository,
) : Telemetry.Sink {

    @Volatile
    private var deviceId: String = ""

    override fun insertSync(e: TelemetryEvent) {
        runCatching { repo.insert(e, deviceMeta()) }
    }

    override fun requestUpload() {
        TelemetryWork.enqueue(context)
    }

    override fun deviceMeta(): DeviceMeta = DeviceMeta(
        deviceId = deviceId,
        appVersion = BuildConfig.APP_VERSION,
        osVersion = "Android ${Build.VERSION.RELEASE}",
        model = "${Build.MANUFACTURER} ${Build.MODEL}".trim(),
    )

    override fun updateDeviceId(id: String) {
        deviceId = id
    }
}
