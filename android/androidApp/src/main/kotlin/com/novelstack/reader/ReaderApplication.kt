package com.novelstack.reader

import android.app.Application
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.KeepAliveController
import com.novelstack.reader.app.TelemetryControl
import com.novelstack.reader.data.createReaderStore
import com.novelstack.reader.di.appModule
import com.novelstack.reader.di.platformModule
import com.novelstack.reader.net.ApiEndpointManager
import com.novelstack.reader.net.EndpointStore
import com.novelstack.reader.net.NovelApi
import com.novelstack.reader.platform.AndroidAppLock
import com.novelstack.reader.platform.AndroidKeepAlive
import com.novelstack.reader.platform.AndroidLanDiscovery
import com.novelstack.reader.platform.AndroidPowerMonitor
import com.novelstack.reader.platform.AndroidScreenGuard
import com.novelstack.reader.platform.AndroidTextActions
import com.novelstack.reader.platform.AudioSinkFactory
import com.novelstack.reader.platform.DeviceKeyStore
import com.novelstack.reader.platform.LocalDataWiper
import com.novelstack.reader.platform.NetworkMonitor
import com.novelstack.reader.telemetry.AndroidTelemetrySink
import com.novelstack.reader.telemetry.Telemetry
import com.novelstack.reader.telemetry.TelemetryKind
import com.novelstack.reader.telemetry.TelemetryRepository
import com.novelstack.reader.telemetry.TelemetryStats
import com.novelstack.reader.telemetry.TelemetryWork
import com.novelstack.reader.telemetry.openTelemetryDatabase
import kotlinx.coroutines.CoroutineScope
import kotlinx.serialization.json.Json
import org.koin.android.ext.koin.androidContext
import org.koin.core.context.startKoin

/**
 * 进程级装配。
 *
 * 依赖关系：Keystore → SQLCipher 库 → Ktor → 遥测 → AppState。
 * 注意：数据库密钥只在 createReaderStore / openTelemetryDatabase 内部短暂以 ByteArray 形式存在。
 */
class ReaderApplication : Application() {

    lateinit var state: AppState
        private set

    // 终端防御阶段：Activity 级能力由 MainActivity 绑定/解绑。
    lateinit var appLock: AndroidAppLock
        private set
    lateinit var screenGuard: AndroidScreenGuard
        private set

    private lateinit var networkMonitor: NetworkMonitor
    private var telemetryRepo: TelemetryRepository? = null

    override fun onCreate() {
        super.onCreate()
        val koin = startKoin {
            androidContext(this@ReaderApplication)
            modules(platformModule(this@ReaderApplication), appModule())
        }.koin
        val ctx = this
        // 第十阶段：先装好动态根地址单例，后续 NovelApi / AppState / 拦截器都从它取地址。
        ApiEndpointManager.install(koin.get<EndpointStore>(), koin.get<Json>())
        val keyStore = koin.get<DeviceKeyStore>()

        // ---- 遥测：先装配（崩溃处理器要能在 AppState 之前工作）----
        installTelemetry(keyStore)
        installCrashHandler()
        startAnrWatchdog()
        // 每天一次的本地过期清理（纯本地，无网络约束）。
        TelemetryWork.scheduleDailyCleanup(this)

        val keepAlive: KeepAliveController = AndroidKeepAlive(this)
        val telemetryControl = object : TelemetryControl {
            override fun stats(): TelemetryStats =
                telemetryRepo?.stats() ?: TelemetryStats(0, 0, 0)

            override fun clear() {
                telemetryRepo?.clear()
            }

            override fun uploadNow() {
                // 走 dataSync 前台服务：用户可见、通知栏可感知。
                SyncService.start(this@ReaderApplication)
            }
        }

        // 终端防御：设备能力装配（Activity 引用稍后由 MainActivity 绑定）。
        appLock = AndroidAppLock()
        screenGuard = AndroidScreenGuard()
        val powerMonitor = AndroidPowerMonitor(this)
        val textActions = AndroidTextActions(this)

        state = AppState(
            api = koin.get<NovelApi>(),
            keyStore = keyStore,
            storeFactory = { createReaderStore(ctx, keyStore) },
            wiper = koin.get<LocalDataWiper>(),
            sinkFactory = koin.get<AudioSinkFactory>(),
            json = koin.get<Json>(),
            scope = koin.get<CoroutineScope>(),
            appUpdater = com.novelstack.reader.update.AndroidAppUpdater(this),
            keepAlive = keepAlive,
            telemetryControl = telemetryControl,
            lanDiscovery = AndroidLanDiscovery(this, koin.get<CoroutineScope>()),
            powerMonitor = powerMonitor,
            appLock = appLock,
            screenGuard = screenGuard,
            textActions = textActions,
        )

        networkMonitor = NetworkMonitor(this) { status -> state.onNetworkChanged(status) }
        networkMonitor.start()
        state.onNetworkChanged(networkMonitor.current())
    }

    private fun installTelemetry(keyStore: DeviceKeyStore) {
        runCatching {
            val handle = openTelemetryDatabase(this, keyStore.dbPassphrase())
            val repo = TelemetryRepository(handle.database)
            telemetryRepo = repo
            Telemetry.install(AndroidTelemetrySink(this, repo))
        }
    }

    /**
     * 全局异常捕获：在交给系统默认处理器之前**同步**写库。
     * 顺序不能反——默认处理器会直接杀掉进程。
     */
    private fun installCrashHandler() {
        val previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            runCatching {
                Telemetry.crash(throwable, where = "thread:${thread.name}")
            }
            previous?.uncaughtException(thread, throwable)
        }
    }

    /**
     * 主线程看门狗：每秒 ping 一次主线程，超过 5 秒无响应判定为 ANR。
     * 只记录一次（episode 去重），避免卡死期间刷出成百上千条。
     */
    private fun startAnrWatchdog() {
        val main = Handler(Looper.getMainLooper())
        val state = AnrState()
        Thread {
            while (true) {
                main.post { state.lastPong = SystemClock.uptimeMillis() }
                Thread.sleep(1_000)
                val waited = SystemClock.uptimeMillis() - state.lastPong
                if (waited > 5_000) {
                    if (!state.reported) {
                        state.reported = true
                        Telemetry.record(
                            kind = TelemetryKind.ANR,
                            name = "main_thread_blocked",
                            durationMs = waited,
                            stack = mainThreadStack(),
                            context = mapOf("threshold_ms" to "5000"),
                        )
                    }
                } else {
                    state.reported = false
                }
            }
        }.apply {
            name = "ns-anr-watchdog"
            isDaemon = true
            start()
        }
    }

    /** 看门狗共享状态（匿名对象避免 @Volatile 局部变量的编译限制）。 */
    private class AnrState {
        @Volatile var lastPong: Long = SystemClock.uptimeMillis()
        @Volatile var reported: Boolean = false
    }

    private fun mainThreadStack(): String =
        Looper.getMainLooper().thread.stackTrace.joinToString("\n") { "  at $it" }

    override fun onTerminate() {
        networkMonitor.stop()
        super.onTerminate()
    }
}
