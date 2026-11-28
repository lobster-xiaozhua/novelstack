package com.novelstack.reader.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Slider
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.KeepAliveInfo
import com.novelstack.reader.app.Screen
import com.novelstack.reader.ui.components.ApplePressable
import com.novelstack.reader.ui.components.DestructiveTextButton
import com.novelstack.reader.ui.components.GroupedList
import com.novelstack.reader.ui.components.IosActionSheet
import com.novelstack.reader.ui.components.SheetAction
import com.novelstack.reader.ui.components.SectionHeader
import kotlinx.datetime.Clock

/**
 * 设置页：iOS 分组内嵌列表。
 *
 * 每组 = 灰色小标题 + 白色 G3 卡片；行高 52dp，行间极细分割线左侧缩进 16dp。
 * 开关、滑块、说明文字都收进卡片，避免白底灰底混杂。
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(state: AppState) {
    val server by state.serverUrl.collectAsState()
    val device by state.deviceId.collectAsState()
    val settings by state.settings.collectAsState()
    val lastSync by state.lastSyncAt.collectAsState()
    val update by state.update.collectAsState()
    val telemetry by state.telemetryStats.collectAsState()
    val offline by state.offline.collectAsState()
    val battery by state.power.collectAsState()
    var keepAlive by remember { mutableStateOf<KeepAliveInfo?>(null) }
    var confirmWipe by remember { mutableStateOf(false) }
    var showUpdate by remember { mutableStateOf(false) }

    LaunchedEffect(Unit) {
        keepAlive = state.keepAliveInfo()
        state.refreshTelemetryStats()
    }

    Scaffold(
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            TopAppBar(
                title = { Text("设置", fontWeight = FontWeight.SemiBold) },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.background,
                ),
            )
        },
    ) { pad ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(pad)
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 16.dp),
        ) {
            // ---------- 账号与设备 ----------
            SectionHeader("账号与设备")
            GroupedList {
                InfoRow("服务端", server.ifBlank { "未配对" })
                RowDivider()
                InfoRow("设备 ID", device.ifBlank { "—" })
                RowDivider()
                InfoRow(
                    "最近同步",
                    if (lastSync == 0L) "—"
                    else "${((Clock.System.now().toEpochMilliseconds() - lastSync) / 1000)} 秒前",
                )
                RowDivider()
                InfoRow("前台同步", if (state.foregroundSyncActive()) "每 5 分钟增量拉取" else "仅在打开时拉取")
                RowDivider()
                InfoRow("本地检索", state.ftsModeLabel())
            }

            // ---------- 控制台 ----------
            SectionHeader("控制台")
            GroupedList {
                RowAction(
                    title = "打开 Web 控制台",
                    subtitle = "免密进入（一次性票据，60 秒内有效）",
                    onClick = {
                        state.requestWebConsole { url ->
                            state.screen.value = Screen.WebConsole(url)
                        }
                    },
                )
                RowDivider()
                RowAction(
                    title = "服务器设置",
                    subtitle = "地址切换、剪贴板导入、局域网发现与 6 位码配对",
                    onClick = { state.screen.value = Screen.ServerSettings },
                )
                RowDivider()
                RowAction(
                    title = "使用指南",
                    subtitle = "导入 TXT、多音色听书、局域网连接、多端进度合并",
                    onClick = { state.screen.value = Screen.Guide },
                )
            }

            // ---------- 朗读 ----------
            SectionHeader("朗读")
            GroupedList {
                Column(modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                    Text(
                        "语速 ${formatSpeed(settings.ttsSpeed)}x",
                        style = MaterialTheme.typography.bodyLarge,
                    )
                    Text(
                        "下次开始朗读时生效",
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    Slider(
                        value = settings.ttsSpeed,
                        onValueChange = { state.updateSettings(settings.copy(ttsSpeed = it)) },
                        valueRange = 0.5f..2.0f,
                        steps = 5,
                    )
                }
            }

            // ---------- 安全与省电 ----------
            SectionHeader("安全与省电")
            GroupedList {
                Text(
                    "电量 ${battery.batteryPercent}%${if (battery.isPowerSaveMode) " · 省电模式" else ""}" +
                        " · ${if (battery.isLow) "低电量降级已生效" else "正常"}",
                    style = MaterialTheme.typography.bodySmall,
                    color = if (battery.isLow) MaterialTheme.colorScheme.error
                    else MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 10.dp),
                )
                RowDivider()
                SwitchRow(
                    label = "生物识别应用锁",
                    hint = if (state.appLockCapable()) "后台超 30 秒回前台需重新验证"
                    else "当前设备不支持或未录入生物特征",
                    checked = settings.appLockEnabled && state.appLockCapable(),
                    enabled = state.appLockCapable(),
                ) { state.updateSettings(settings.copy(appLockEnabled = it)) }
                RowDivider()
                SwitchRow(
                    label = "防截屏保护",
                    hint = "阅读/设置页禁止截屏、录屏与最近任务缩略图",
                    checked = settings.secureScreen,
                ) {
                    state.updateSettings(settings.copy(secureScreen = it))
                    state.reapplySecureFlag(Screen.Settings)
                }
                RowDivider()
                SwitchRow(
                    label = "翻页触感",
                    hint = "翻页微振动；低电量时自动关闭",
                    checked = settings.hapticsEnabled,
                ) { state.updateSettings(settings.copy(hapticsEnabled = it)) }
                RowDivider()
                SwitchRow(
                    label = "移动网络省流量封面",
                    hint = "3G/4G 或低电量时封面只显示轻量占位",
                    checked = settings.lowDataCover,
                ) { state.updateSettings(settings.copy(lowDataCover = it)) }
            }

            // ---------- 阅读统计 ----------
            SectionHeader("阅读")
            GroupedList {
                RowAction(
                    title = "阅读统计",
                    subtitle = "今日/累计时长、连续天数、30 天柱状图与 90 天热力图",
                    onClick = {
                        state.loadReadingStats()
                        state.screen.value = Screen.Stats
                    },
                )
                if (offline) {
                    Text(
                        "离线模式：显示上次同步的数据",
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.error,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                    )
                }
            }

            // ---------- 应用更新 ----------
            SectionHeader("应用更新")
            GroupedList {
                InfoRow("当前版本", AppState.APP_VERSION)
                RowDivider()
                RowAction(
                    title = "检查更新",
                    subtitle = if (update.checking) "正在检查…" else "并发测速多镜像，校验 SHA-256 后安装",
                    trailing = {
                        if (update.checking) {
                            CircularProgressIndicator(modifier = Modifier.size(18.dp), strokeWidth = 2.dp)
                        }
                    },
                    onClick = {
                        state.checkUpdate()
                        showUpdate = true
                    },
                )
            }

            // ---------- 系统保活 ----------
            SectionHeader("系统保活")
            GroupedList {
                Text(
                    "国产 ROM 会主动清理后台，导致朗读或同步中断。建议完成下面两步：",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 10.dp),
                )
                RowDivider()
                InfoRow("机型", keepAlive?.deviceLabel ?: "—")
                RowDivider()
                InfoRow("电池优化", if (keepAlive?.batteryIgnored == true) "已忽略（推荐）" else "未忽略")
                RowDivider()
                RowAction(
                    title = "忽略电池优化",
                    subtitle = "允许后台朗读与同步持续运行",
                    onClick = { state.requestIgnoreBatteryOptimizations() },
                )
                RowDivider()
                RowAction(
                    title = "自启动设置",
                    subtitle = "跳转到系统自启动管理页",
                    onClick = {
                        state.openAutostartSettings()
                        keepAlive = state.keepAliveInfo()
                    },
                )
                keepAlive?.hint?.takeIf { it.isNotBlank() }?.let {
                    Text(
                        it,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp),
                    )
                }
            }

            // ---------- 遥测与诊断 ----------
            SectionHeader("遥测与诊断")
            GroupedList {
                Text(
                    "只上报运行状况、机型与耗时，绝不含书籍正文、音频和任何 Token。" +
                        "数据先落盘到独立加密库，仅在 WiFi 下批量上传。",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 10.dp),
                )
                RowDivider()
                val stats = telemetry
                InfoRow("待上传", if (stats == null) "—" else "${stats.rows} 条 / ${formatBytes(stats.bytes)}")
                RowDivider()
                InfoRow("其中崩溃", stats?.crashes?.toString() ?: "—")
                RowDivider()
                RowAction(
                    title = "立即上传",
                    subtitle = "仅在 WiFi 连接时真正发出",
                    onClick = {
                        state.uploadTelemetryNow()
                        state.refreshTelemetryStats()
                    },
                )
                RowDivider()
                Row(
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp),
                    horizontalArrangement = Arrangement.End,
                ) {
                    DestructiveTextButton(
                        text = "清空本地遥测",
                        onClick = { state.clearTelemetry() },
                    )
                }
            }

            // ---------- 配对说明 ----------
            SectionHeader("配对与撤销")
            GroupedList {
                Text(
                    "新设备：控制台 → 设备 → 生成一次性配对码，在登录页输入。\n" +
                        "撤销：撤销后本机下一次请求会收到 401 并回到配对页。\n" +
                        "device token 与浏览器会话相互独立，撤销一方不影响另一方。",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(16.dp),
                )
            }

            // ---------- 危险操作 ----------
            SectionHeader("危险操作")
            GroupedList {
                Text(
                    "清除本地 SQLCipher 数据库、Keystore 设备密钥与 device token。" +
                        "服务端数据与阅读进度不受影响；清除后需重新配对。",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 10.dp),
                )
                RowDivider()
                Row(
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 8.dp, vertical = 4.dp),
                    horizontalArrangement = Arrangement.End,
                ) {
                    DestructiveTextButton(text = "退出并清除本地数据", onClick = { confirmWipe = true })
                }
            }

            Spacer(Modifier.height(28.dp))
            Text(
                "NovelStack Reader ${AppState.APP_VERSION}\n" +
                    "苹果风视觉 / 前后台分离 / Web 阅读器 / 多端同步。",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 4.dp, vertical = 4.dp),
            )
            Spacer(Modifier.height(24.dp))
        }
    }

    // 危险操作统一走底部确认菜单：不在页面上放「大红按钮」，点进来还要再确认一次。
    IosActionSheet(
        visible = confirmWipe,
        title = "退出并清除本地数据？",
        message = "将删除本机缓存的章节、SQLCipher 数据库、Keystore 设备密钥与 device token。服务端数据不受影响，但需要重新配对。",
        actions = listOf(
            SheetAction("清除并退出", danger = true) {
                state.logoutAndWipe()
            },
        ),
        onDismiss = { confirmWipe = false },
    )

    if (showUpdate) {
        AlertDialog(
            onDismissRequest = { showUpdate = false },
            title = {
                Text(
                    when {
                        update.checking -> "检查更新"
                        update.hasUpdate -> "发现新版本 ${update.info?.version ?: ""}"
                        else -> "检查更新"
                    },
                )
            },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    if (update.checking) {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            CircularProgressIndicator(modifier = Modifier.size(16.dp), strokeWidth = 2.dp)
                            Spacer(Modifier.width(8.dp))
                            Text("正在检查…", style = MaterialTheme.typography.bodySmall)
                        }
                    } else {
                        update.message?.let { Text(it, style = MaterialTheme.typography.bodyMedium) }
                        update.info?.changelog?.takeIf { it.isNotBlank() }?.let {
                            Text("更新说明：", style = MaterialTheme.typography.labelMedium)
                            Text(it, style = MaterialTheme.typography.bodySmall)
                        }
                        if (update.hasUpdate) {
                            val mirrors = update.info?.mirrors ?: emptyList()
                            Text(
                                if (mirrors.isEmpty()) "服务端未配置镜像，暂时无法下载。"
                                else "可用镜像 ${mirrors.size} 个",
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                    }
                    if (update.downloading) {
                        LinearProgressIndicator(
                            progress = { update.progress.coerceIn(0f, 1f) },
                            modifier = Modifier.fillMaxWidth(),
                        )
                        Text(
                            "下载中 ${(update.progress * 100).toInt()}%",
                            style = MaterialTheme.typography.bodySmall,
                        )
                    }
                }
            },
            confirmButton = {
                if (update.hasUpdate && !update.downloading) {
                    TextButton(onClick = { state.downloadUpdate() }) { Text("下载并安装") }
                } else {
                    TextButton(onClick = { showUpdate = false }) { Text("关闭") }
                }
            },
            dismissButton = {
                if (update.hasUpdate && !update.downloading) {
                    TextButton(onClick = { showUpdate = false }) { Text("取消") }
                }
            },
        )
    }
}

/** 行间分割线：左侧缩进 16dp，与文字左缘齐平。 */
@Composable
private fun RowDivider() {
    HorizontalDivider(
        modifier = Modifier.padding(start = 16.dp),
        thickness = 0.5.dp,
        color = MaterialTheme.colorScheme.outlineVariant,
    )
}

@Composable
private fun InfoRow(label: String, value: String) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(
            label,
            style = MaterialTheme.typography.bodyLarge,
            color = MaterialTheme.colorScheme.onSurface,
            modifier = Modifier.weight(1f),
        )
        Text(
            value,
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            fontWeight = FontWeight.Medium,
        )
    }
}

@Composable
private fun RowAction(
    title: String,
    subtitle: String? = null,
    trailing: (@Composable () -> Unit)? = null,
    onClick: () -> Unit,
) {
    ApplePressable(onClick = onClick, shape = androidx.compose.foundation.shape.RoundedCornerShape(0.dp)) {
        Row(
            modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Text(title, style = MaterialTheme.typography.bodyLarge)
                if (!subtitle.isNullOrBlank()) {
                    Text(
                        subtitle,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
            if (trailing != null) trailing()
            else Text(
                "›",
                style = MaterialTheme.typography.titleLarge,
                color = AppleTokens.TertiaryLabel,
            )
        }
    }
}

@Composable
private fun SwitchRow(
    label: String,
    hint: String,
    checked: Boolean,
    enabled: Boolean = true,
    onChange: (Boolean) -> Unit,
) {
    Row(
        modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 8.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(modifier = Modifier.weight(1f)) {
            Text(label, style = MaterialTheme.typography.bodyLarge)
            Text(
                hint,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        Switch(checked = checked, enabled = enabled, onCheckedChange = onChange)
    }
}

private fun formatSpeed(v: Float): String {
    val n = (v * 10).toInt()
    return "${n / 10}.${kotlin.math.abs(n % 10)}"
}
