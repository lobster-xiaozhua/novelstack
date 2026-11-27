package com.novelstack.reader.app

import com.novelstack.reader.telemetry.TelemetryStats

/** 网络状态：决定是否允许预读/同步等大流量动作，以及顶部横幅文案。 */
enum class NetworkStatus {
    /** WiFi 或以太网（不计量）。 */
    Online,

    /** 移动网络：可用但不跑大流量（预读、遥测上传）。 */
    Metered,

    Offline,
}

/**
 * 系统保活能力（Android 实现见 androidMain/AndroidKeepAlive）。
 * 抽成接口是为了让 Compose 层与平台解耦，iOS 传 null 即可。
 */
interface KeepAliveController {
    fun batteryOptimizationIgnored(): Boolean
    fun requestIgnoreBatteryOptimizations()

    /** 返回 true 表示成功打开厂商自启动页；false 表示已退回应用详情页。 */
    fun openAutostartSettings(): Boolean
    fun deviceLabel(): String

    /** 针对当前 ROM 的文字指引（不同厂商菜单名差异很大）。 */
    fun vendorHint(): String
}

/** 保活页展示快照（一次性读取，避免 Compose 里反复查系统服务）。 */
data class KeepAliveInfo(
    val deviceLabel: String,
    val batteryIgnored: Boolean,
    val hint: String,
)

/**
 * 遥测控制面板能力：统计、清空、立即上传。
 * Android 实现同时负责启动 dataSync 前台服务做可见的「立即同步」。
 */
interface TelemetryControl {
    fun stats(): TelemetryStats
    fun clear()
    fun uploadNow()
}
