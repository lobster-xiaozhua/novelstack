package com.novelstack.reader.app

/**
 * 终端防御阶段：设备侧平台能力抽象。
 *
 * 全部定义在 commonMain，Android 在 androidMain 提供实现，其它平台可实现为 no-op。
 * 这样 AppState 与 Compose UI 不直接依赖 Android API，便于 commonTest 验证纯逻辑。
 */

/** 电池快照：低电量/省电模式驱动预读、遥测与 TTS 缓冲的降级。 */
data class PowerSnapshot(
    val batteryPercent: Int = 100,
    val isPowerSaveMode: Boolean = false,
    val isCharging: Boolean = false,
) {
    /** 低电量阈值：20%（与需求一致）。充电时不触发降级。 */
    val isLow: Boolean get() = !isCharging && (batteryPercent in 1..19 || isPowerSaveMode)
}

/** 电池状态监听：注册后回调当前值与后续变化。 */
interface PowerMonitor {
    fun current(): PowerSnapshot
    /** 注册监听，返回注销句柄。 */
    fun register(onChange: (PowerSnapshot) -> Unit): () -> Unit
}

/** 无实现时的默认：永远满电，不触发任何降级。 */
object NoopPowerMonitor : PowerMonitor {
    override fun current(): PowerSnapshot = PowerSnapshot()
    override fun register(onChange: (PowerSnapshot) -> Unit): () -> Unit = {}
}

/**
 * 生物识别应用锁。
 * 平台实现用 BiometricPrompt（指纹/面容/设备凭证），只返回成功/失败，不经手任何密钥。
 */
interface AppLockController {
    /** 设备是否支持生物识别。 */
    fun isAvailable(): Boolean
    /** 用户是否已录入生物特征或设置设备凭证。 */
    fun isEnrolled(): Boolean
    /**
     * 发起验证。结果通过回调返回（主线程调用由实现保证）。
     * @param onSuccess 验证通过
     * @param onError 失败/取消，errorCode 供 UI 选择文案
     */
    fun authenticate(onSuccess: () -> Unit, onError: (code: Int, message: String) -> Unit)
}

/** 无生物识别能力时默认放行（避免无指纹设备把用户锁死）。 */
object NoopAppLock : AppLockController {
    override fun isAvailable(): Boolean = false
    override fun isEnrolled(): Boolean = false
    override fun authenticate(onSuccess: () -> Unit, onError: (Int, String) -> Unit) = onSuccess()
}

/**
 * 防截屏：在阅读页/设置页给 Window 加/清 FLAG_SECURE。
 * 这是当前 Activity 窗口级开关，由平台实现直接操作 window。
 */
interface ScreenGuard {
    fun setSecure(enabled: Boolean)
}

object NoopScreenGuard : ScreenGuard {
    override fun setSecure(enabled: Boolean) {}
}

/**
 * 文本操作：长按查词的平台侧动作。
 * 复制走 ClipboardManager；网页翻译走 ACTION_PROCESS_TEXT；本地搜索由 AppState 处理。
 */
interface TextActions {
    /** 复制文本到系统剪贴板。 */
    fun copy(text: String)
    /** 通过 ACTION_PROCESS_TEXT 调用系统翻译/词典。返回 false 表示无可用应用。 */
    fun processText(text: String): Boolean
}

object NoopTextActions : TextActions {
    override fun copy(text: String) {}
    override fun processText(text: String): Boolean = false
}

/**
 * 发现页封面加载策略：仅 WiFi（不计量）且电量正常时加载高清封面；
 * 移动网络或低电量时只显示轻量占位（本 App 封面本就是文字占位，策略决定「 rich 展示」与否）。
 */
object CoverLoadPolicy {
    enum class Quality { FULL, LOW }

    fun decide(network: NetworkStatus, lowPower: Boolean, lowDataEnabled: Boolean): Quality {
        // 开关关闭时总是高清；否则移动网络/离线/低电量降级为低清占位。
        if (!lowDataEnabled) return Quality.FULL
        if (network != NetworkStatus.Online) return Quality.LOW
        if (lowPower) return Quality.LOW
        return Quality.FULL
    }
}

/**
 * TTS 解码缓冲策略：低电量时缩小 AudioTrack 缓冲倍数，降低内存与 CPU 占用。
 * 进程级可变状态，由 AppState 在电量变化时更新，AudioSink 在 start() 时读取。
 */
object AudioBufferPolicy {
    /** 正常倍数。 */
    const val NORMAL_MULTIPLIER = 2
    /** 低电量/省电模式倍数（接近系统最小值，减少缓冲内存）。 */
    const val LOW_MULTIPLIER = 1

    @Volatile
    var lowPower: Boolean = false

    fun multiplier(): Int = if (lowPower) LOW_MULTIPLIER else NORMAL_MULTIPLIER
}

/** 触觉反馈强度策略：低电量时降低触发频率（纯逻辑，可单测）。 */
object HapticPolicy {
    /** 连续翻页时，最小间隔（毫秒）；低于此值不重复振动，避免长列表滑动刷振。 */
    const val MIN_INTERVAL_MS = 60L

    fun shouldVibrate(lastAtMs: Long, nowMs: Long, enabled: Boolean, lowPower: Boolean): Boolean {
        if (!enabled) return false
        if (lowPower) return false // 低电量完全关闭触感，省电
        return nowMs - lastAtMs >= MIN_INTERVAL_MS
    }
}
