package com.novelstack.reader.platform

import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.PowerManager
import android.provider.Settings
import com.novelstack.reader.app.KeepAliveController

/**
 * 系统保活引导。
 *
 * 国产 ROM 没有统一的「自启动」开关入口，只能按厂商逐个试。
 * 所有 Intent 都用 resolveActivity 判空后再启动：找不到就退回应用详情页，
 * 绝不因为跳转失败而崩溃。
 */
class AndroidKeepAlive(private val context: Context) : KeepAliveController {

    override fun batteryOptimizationIgnored(): Boolean {
        val pm = context.getSystemService(PowerManager::class.java) ?: return false
        return pm.isIgnoringBatteryOptimizations(context.packageName)
    }

    override fun requestIgnoreBatteryOptimizations() {
        val intent = Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS)
            .setData(Uri.parse("package:${context.packageName}"))
        if (!start(intent)) openAppDetails()
    }

    /** 按厂商尝试自启动管理页；全部失败时打开应用详情页。返回是否命中厂商页面。 */
    override fun openAutostartSettings(): Boolean {
        for (intent in vendorIntents()) {
            if (start(intent)) return true
        }
        openAppDetails()
        return false
    }

    override fun deviceLabel(): String =
        "${Build.MANUFACTURER} ${Build.MODEL}".trim()

    override fun vendorHint(): String = when (Build.MANUFACTURER.lowercase()) {
        "xiaomi", "redmi" -> "小米/红米：安全中心 → 应用管理 → 权限 → 自启动，并允许后台弹出界面"
        "huawei", "honor" -> "华为/荣耀：手机管家 → 应用启动管理 → 关闭自动管理，手动允许自启动/关联启动"
        "oppo", "realme", "oneplus" -> "OPPO/realme/一加：设置 → 电池 → 应用耗电管理 → 允许后台运行"
        "vivo", "iqoo" -> "vivo/iQOO：设置 → 电池 → 后台耗电管理 → 允许后台高耗电"
        "samsung" -> "三星：设置 → 电池 → 后台使用限制 → 从休眠名单移除"
        else -> "请在系统设置中允许本应用自启动与后台运行"
    }

    private fun vendorIntents(): List<Intent> {
        val mfr = Build.MANUFACTURER.lowercase()
        val components = when (mfr) {
            "xiaomi", "redmi" -> listOf(
                "com.miui.securitycenter/com.miui.permcenter.autostart.AutoStartManagementActivity",
                "com.miui.powerkeeper/com.miui.powerkeeper.ui.HiddenAppsConfigActivity",
            )
            "huawei", "honor" -> listOf(
                "com.huawei.systemmanager/.startupmgr.ui.StartupNormalAppListActivity",
                "com.huawei.systemmanager/.optimize.process.ProtectActivity",
            )
            "oppo", "realme", "oneplus" -> listOf(
                "com.coloros.safecenter/.permission.startup.StartupAppListActivity",
                "com.oplus.safecenter/.permission.startup.StartupAppListActivity",
                "com.oppo.safe/.permission.startup.StartupAppListActivity",
            )
            "vivo", "iqoo" -> listOf(
                "com.vivo.permissionmanager/.activity.BgStartUpManagerActivity",
                "com.iqoo.secure/.ui.phoneoptimize.AddWhiteListActivity",
            )
            "samsung" -> listOf(
                "com.samsung.android.lool/com.samsung.android.sm.ui.battery.BatteryActivity",
            )
            else -> emptyList()
        }
        return components.mapNotNull { spec ->
            val parts = spec.split("/")
            if (parts.size != 2) return@mapNotNull null
            val pkg = parts[0]
            val cls = if (parts[1].startsWith(".")) pkg + parts[1] else parts[1]
            Intent().setComponent(ComponentName(pkg, cls))
        }
    }

    private fun start(intent: Intent): Boolean {
        return try {
            if (intent.resolveActivity(context.packageManager) == null) return false
            if (context !is android.app.Activity) intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
            context.startActivity(intent)
            true
        } catch (_: Throwable) {
            false
        }
    }

    private fun openAppDetails() {
        start(
            Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS)
                .setData(Uri.parse("package:${context.packageName}")),
        )
    }
}
