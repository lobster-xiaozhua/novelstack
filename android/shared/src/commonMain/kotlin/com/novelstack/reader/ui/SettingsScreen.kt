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
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
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
import kotlinx.datetime.Clock

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(state: AppState) {
    val server by state.serverUrl.collectAsState()
    val device by state.deviceId.collectAsState()
    val settings by state.settings.collectAsState()
    val lastSync by state.lastSyncAt.collectAsState()
    val update by state.update.collectAsState()
    var confirmWipe by remember { mutableStateOf(false) }
    var showUpdate by remember { mutableStateOf(false) }

    Scaffold(
        topBar = {
            TopAppBar(title = { Text("设置") })
        },
    ) { pad ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(pad)
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 18.dp, vertical = 10.dp),
            verticalArrangement = Arrangement.spacedBy(10.dp),
        ) {
            Text("账号与设备", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
            InfoRow("服务端", server.ifBlank { "未配对" })
            InfoRow("设备 ID", device.ifBlank { "—" })
            InfoRow(
                "最近同步",
                if (lastSync == 0L) "—" else "${((Clock.System.now().toEpochMilliseconds() - lastSync) / 1000)} 秒前",
            )
            InfoRow("本地检索", state.ftsModeLabel())

            HorizontalDivider(Modifier.padding(vertical = 6.dp))
            Text("朗读", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
            Text(
                "语速 ${formatSpeed(settings.ttsSpeed)}x（下次朗读生效）",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Slider(
                value = settings.ttsSpeed,
                onValueChange = { state.updateSettings(settings.copy(ttsSpeed = it)) },
                valueRange = 0.5f..2.0f,
                steps = 5,
            )

            HorizontalDivider(Modifier.padding(vertical = 6.dp))
            Text("配对与撤销", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
            Text(
                "• 新设备：在浏览器打开 /admin → 设备 → 生成一次性配对码，在登录页输入。\n" +
                    "• 撤销：在同一页面撤销设备后，本机下一次请求会收到 401 并回到配对页。\n" +
                    "• device token 与浏览器会话相互独立，撤销一方不影响另一方。",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )

            HorizontalDivider(Modifier.padding(vertical = 6.dp))
            Text("应用更新", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
            InfoRow("当前版本", AppState.APP_VERSION)
            Row(verticalAlignment = Alignment.CenterVertically) {
                TextButton(onClick = {
                    state.checkUpdate()
                    showUpdate = true
                }) { Text("检查更新") }
                if (update.checking) {
                    Spacer(Modifier.width(8.dp))
                    CircularProgressIndicator(modifier = Modifier.size(16.dp), strokeWidth = 2.dp)
                }
            }
            Text(
                "下载会并发对多镜像测速（各取前 100KB），选最快者；完成后校验 SHA-256，" +
                    "通过才唤起系统安装器（FileProvider）。",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )

            HorizontalDivider(Modifier.padding(vertical = 6.dp))
            Text("危险操作", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
            Text(
                "清除本地 SQLCipher 数据库、Keystore 内的设备密钥与 device token。" +
                    "服务端数据与阅读进度不受影响；清除后必须重新配对。",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            TextButton(onClick = { confirmWipe = true }) {
                Text("退出并清除本地数据", color = MaterialTheme.colorScheme.error)
            }

            Spacer(Modifier.height(24.dp))
            Text(
                "NovelStack Reader ${AppState.APP_VERSION} · 第八阶段\n" +
                    "发现 / 足迹 / 收藏 / 应用内更新（测速 + 哈希校验）。",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }

    if (confirmWipe) {
        AlertDialog(
            onDismissRequest = { confirmWipe = false },
            title = { Text("确认清除本地数据？") },
            text = { Text("此操作不可撤销，将删除本机全部已缓存章节与设备凭证。") },
            confirmButton = {
                TextButton(onClick = {
                    confirmWipe = false
                    state.logoutAndWipe()
                }) { Text("清除", color = MaterialTheme.colorScheme.error) }
            },
            dismissButton = { TextButton(onClick = { confirmWipe = false }) { Text("取消") } },
        )
    }

    if (showUpdate) {
        AlertDialog(
            onDismissRequest = { showUpdate = false },
            title = {
                val title = when {
                    update.checking -> "检查更新"
                    update.hasUpdate -> "发现新版本 ${update.info?.version ?: ""}"
                    else -> "检查更新"
                }
                Text(title)
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
                        update.message?.let {
                            Text(it, style = MaterialTheme.typography.bodyMedium)
                        }
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

@Composable
private fun InfoRow(label: String, value: String) {
    Row(modifier = Modifier.fillMaxWidth()) {
        Text(
            label,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.weight(1f),
        )
        Text(value, style = MaterialTheme.typography.bodySmall, fontWeight = FontWeight.Medium)
    }
}

private fun formatSpeed(v: Float): String {
    val n = (v * 10).toInt()
    return "${n / 10}.${kotlin.math.abs(n % 10)}"
}
