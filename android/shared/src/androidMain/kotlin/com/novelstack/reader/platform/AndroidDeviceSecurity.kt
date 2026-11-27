package com.novelstack.reader.platform

import android.app.Activity
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.os.BatteryManager
import android.os.PowerManager
import androidx.biometric.BiometricManager
import androidx.biometric.BiometricManager.Authenticators.BIOMETRIC_STRONG
import androidx.biometric.BiometricManager.Authenticators.DEVICE_CREDENTIAL
import androidx.biometric.BiometricPrompt
import androidx.core.content.ContextCompat
import androidx.fragment.app.FragmentActivity
import com.novelstack.reader.app.AppLockController
import com.novelstack.reader.app.PowerMonitor
import com.novelstack.reader.app.PowerSnapshot
import com.novelstack.reader.app.ScreenGuard
import com.novelstack.reader.app.TextActions

/**
 * 生物识别应用锁：BiometricPrompt（指纹/面容 + 设备凭证兜底）。
 *
 * 只返回验证结果，不接触任何密钥——正文密钥仍在 Android Keystore，与解锁无关。
 * Activity 由 MainActivity 在 onCreate 绑定、onDestroy 解绑（进程级单例被 Activity 重建时仍有效）。
 * BiometricPrompt 需要 FragmentActivity，宿主 MainActivity 已改为继承它。
 */
class AndroidAppLock : AppLockController {
    @Volatile
    private var activity: FragmentActivity? = null
    private var onSuccess: (() -> Unit)? = null
    private var onError: ((Int, String) -> Unit)? = null

    /** 由 MainActivity 绑定当前 Activity。 */
    fun attach(activity: FragmentActivity?) { this.activity = activity }

    private val authenticators = BIOMETRIC_STRONG or DEVICE_CREDENTIAL

    override fun isAvailable(): Boolean {
        val a = activity ?: return false
        return try {
            BiometricManager.from(a).canAuthenticate(authenticators) == BiometricManager.BIOMETRIC_SUCCESS
        } catch (_: Throwable) {
            false
        }
    }

    override fun isEnrolled(): Boolean = isAvailable()

    override fun authenticate(onSuccess: () -> Unit, onError: (Int, String) -> Unit) {
        val a = activity
        if (a == null) {
            onError(-1, "应用未就绪")
            return
        }
        this.onSuccess = onSuccess
        this.onError = onError
        try {
            val prompt = BiometricPrompt(
                a,
                ContextCompat.getMainExecutor(a),
                object : BiometricPrompt.AuthenticationCallback() {
                    override fun onAuthenticationSucceeded(result: BiometricPrompt.AuthenticationResult) {
                        onSuccess()
                    }
                    override fun onAuthenticationError(errorCode: Int, errString: CharSequence) {
                        onError(errorCode, errString.toString())
                    }
                    override fun onAuthenticationFailed() {
                        // 单次指纹不匹配，系统允许重试；只有 onAuthenticationError 才是终局。
                    }
                },
            )
            val info = BiometricPrompt.PromptInfo.Builder()
                .setTitle("解锁小说站")
                .setSubtitle("验证指纹、面容或设备密码")
                .setAllowedAuthenticators(authenticators)
                .build()
            prompt.authenticate(info)
        } catch (e: Throwable) {
            onError(-1, e.message ?: "生物识别不可用")
        }
    }
}

/**
 * 电量监听：BatteryManager 广播 + PowerManager.isPowerSaveMode。
 * 用动态注册，注销句柄由 AppState 在退出时释放。
 */
class AndroidPowerMonitor(private val context: Context) : PowerMonitor {
    private val powerManager: PowerManager =
        context.getSystemService(Context.POWER_SERVICE) as PowerManager

    override fun current(): PowerSnapshot {
        return try {
            val bm = context.getSystemService(Context.BATTERY_SERVICE) as BatteryManager
            val level = bm.getIntProperty(BatteryManager.BATTERY_PROPERTY_CAPACITY)
            val charging = batteryCharging(context)
            PowerSnapshot(
                batteryPercent = level.coerceIn(0, 100),
                isPowerSaveMode = powerManager.isPowerSaveMode,
                isCharging = charging,
            )
        } catch (_: Throwable) {
            PowerSnapshot()
        }
    }

    override fun register(onChange: (PowerSnapshot) -> Unit): () -> Unit {
        val filter = IntentFilter().apply {
            addAction(Intent.ACTION_BATTERY_CHANGED)
            addAction(Intent.ACTION_POWER_CONNECTED)
            addAction(Intent.ACTION_POWER_DISCONNECTED)
            addAction(PowerManager.ACTION_POWER_SAVE_MODE_CHANGED)
        }
        val receiver = object : BroadcastReceiver() {
            override fun onReceive(ctx: Context, intent: Intent) = onChange(current())
        }
        ContextCompat.registerReceiver(
            context, receiver, filter, ContextCompat.RECEIVER_NOT_EXPORTED,
        )
        return { context.unregisterReceiver(receiver) }
    }
}

/** 防截屏：给 Activity Window 加/清 FLAG_SECURE。 */
class AndroidScreenGuard : ScreenGuard {
    @Volatile
    private var activity: Activity? = null
    fun attach(activity: Activity?) { this.activity = activity }

    private val flag = android.view.WindowManager.LayoutParams.FLAG_SECURE
    override fun setSecure(enabled: Boolean) {
        val w = activity?.window ?: return
        if (enabled) w.addFlags(flag) else w.clearFlags(flag)
    }
}

/** 文本操作：系统剪贴板 + ACTION_PROCESS_TEXT。 */
class AndroidTextActions(private val context: Context) : TextActions {
    override fun copy(text: String) {
        val cm = context.getSystemService(Context.CLIPBOARD_SERVICE) as android.content.ClipboardManager
        cm.setPrimaryClip(android.content.ClipData.newPlainText("novelstack-selection", text))
    }

    override fun processText(text: String): Boolean {
        return try {
            val intent = Intent(Intent.ACTION_PROCESS_TEXT).apply {
                type = "text/plain"
                putExtra(Intent.EXTRA_PROCESS_TEXT, text)
                putExtra(Intent.EXTRA_PROCESS_TEXT_READONLY, true)
            }
            val chooser = Intent.createChooser(intent, "选择翻译/词典")
            chooser.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            context.startActivity(chooser)
            true
        } catch (_: Throwable) {
            false
        }
    }
}

/**
 * 是否处于充电（含充满）状态。
 *
 * `BatteryManager.BATTERY_PROPERTY_CHARGING` 是 @hide 常量，编译期不可见，
 * 因此改读 ACTION_BATTERY_CHANGED 的 sticky 广播（注册 receiver 传 null 即同步取最后一条）。
 * 取不到时按「未充电」处理——宁可多省一点电，也不要误开预读。
 */
internal fun batteryCharging(context: Context): Boolean = try {
    val sticky = context.registerReceiver(null, IntentFilter(Intent.ACTION_BATTERY_CHANGED))
    when (sticky?.getIntExtra(BatteryManager.EXTRA_STATUS, -1)) {
        BatteryManager.BATTERY_STATUS_CHARGING,
        BatteryManager.BATTERY_STATUS_FULL,
        -> true
        else -> false
    }
} catch (_: Throwable) {
    false
}
