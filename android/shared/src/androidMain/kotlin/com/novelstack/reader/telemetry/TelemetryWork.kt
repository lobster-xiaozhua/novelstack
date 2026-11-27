package com.novelstack.reader.telemetry

import android.content.Context
import androidx.work.BackoffPolicy
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import kotlinx.serialization.json.Json
import java.util.concurrent.TimeUnit

/**
 * 分级上传策略：
 *  - 只在 UNMETERED（WiFi/以太网）执行，绝不偷跑蜂窝流量；
 *  - 每次进前台 enqueue 一次性任务，重复 enqueue 用 KEEP 去重；
 *  - 失败按指数退避重试，鉴权失败则直接放弃本轮（等下次登录后再传）；
 *  - 低电量/省电模式：不再用 WorkManager 的 batteryNotLow 约束一刀切（那样连崩溃都传不了），
 *    改在 worker 内判断——只上传崩溃/ANR，非致命事件静默堆积，且失败不重试。
 */
object TelemetryWork {

    private const val CLEANUP_WORK = "novelstack-telemetry-cleanup"

    fun constraints(): Constraints = Constraints.Builder()
        .setRequiredNetworkType(NetworkType.UNMETERED)
        .build()

    /**
     * 低电量判定（纯逻辑，便于单测）。
     *
     * 这里刻意不用 WorkManager 的 batteryNotLow 约束：那会让低电量时连崩溃都传不出去。
     * 改为 worker 内判断后降级为「只传崩溃、失败不重试」。充电中一律不算低电量。
     */
    fun isLowPower(charging: Boolean, percent: Int, powerSaveMode: Boolean): Boolean =
        !charging && (percent in 1..19 || powerSaveMode)

    fun enqueue(context: Context) {
        val req = OneTimeWorkRequestBuilder<TelemetryUploadWorker>()
            .setConstraints(constraints())
            .setBackoffCriteria(BackoffPolicy.EXPONENTIAL, 30, TimeUnit.MINUTES)
            .build()
        WorkManager.getInstance(context)
            .enqueueUniqueWork(TelemetryPolicy.WORK_NAME, ExistingWorkPolicy.KEEP, req)
    }

    /** 每天一次本地过期清理（纯本地操作，不产生流量，无网络约束）。 */
    fun scheduleDailyCleanup(context: Context) {
        val req = PeriodicWorkRequestBuilder<TelemetryCleanupWorker>(1, TimeUnit.DAYS).build()
        WorkManager.getInstance(context)
            .enqueueUniquePeriodicWork(CLEANUP_WORK, ExistingPeriodicWorkPolicy.KEEP, req)
    }
}

/**
 * 上传 worker。运行在独立入口，需自行装配：Keystore → 业务库（读凭证）→ 遥测库。
 */
class TelemetryUploadWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {

    private val json = Json { ignoreUnknownKeys = true }

    override suspend fun doWork(): Result {
        val ctx = applicationContext
        // 低电量/省电模式：只传崩溃，且任何失败都不重试（禁止非致命数据的上传重试）。
        val pm = ctx.getSystemService(android.content.Context.POWER_SERVICE) as android.os.PowerManager
        val bm = ctx.getSystemService(android.content.Context.BATTERY_SERVICE) as android.os.BatteryManager
        val charging = com.novelstack.reader.platform.batteryCharging(ctx)
        val percent = bm.getIntProperty(android.os.BatteryManager.BATTERY_PROPERTY_CAPACITY)
        val lowPower = TelemetryWork.isLowPower(charging, percent, pm.isPowerSaveMode)
        return try {
            val session = AndroidTelemetryRuntime.open(ctx) ?: return Result.success()
            session.use {
                val uploader = TelemetryUploader(it.api, it.repo, json)
                uploader.cleanupLocal()
                uploader.uploadOnce(crashOnly = lowPower)
            }
            Result.success()
        } catch (e: com.novelstack.reader.model.ApiException) {
            // 凭证失效或低电量时继续重试只会白耗电，本轮直接结束。
            if (e.unauthorized || lowPower) Result.success() else Result.retry()
        } catch (_: Throwable) {
            if (lowPower) Result.success() else Result.retry()
        }
    }
}

/** 每日清理：删除 7 天前仍未上传的记录，防止长期无 WiFi 时无限膨胀。 */
class TelemetryCleanupWorker(
    appContext: Context,
    params: WorkerParameters,
) : CoroutineWorker(appContext, params) {
    override suspend fun doWork(): Result {
        val ctx = applicationContext
        return try {
            val session = AndroidTelemetryRuntime.open(ctx) ?: return Result.success()
            session.use {
                val now = System.currentTimeMillis()
                it.repo.enforceCap(now)
                it.repo.cleanup(now)
            }
            Result.success()
        } catch (_: Throwable) {
            Result.retry()
        }
    }
}
