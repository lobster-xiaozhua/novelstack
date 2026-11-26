package com.novelstack.reader.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
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
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.Screen
import kotlinx.datetime.Clock

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SettingsScreen(state: AppState) {
    val server by state.serverUrl.collectAsState()
    val device by state.deviceId.collectAsState()
    val settings by state.settings.collectAsState()
    val lastSync by state.lastSyncAt.collectAsState()
    var confirmWipe by remember { mutableStateOf(false) }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("设置") },
                navigationIcon = {
                    TextButton(onClick = { state.screen.value = Screen.Library }) { Text("‹ 书库") }
                },
            )
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
                "NovelStack Reader 0.1.0 · 第五阶段（路线 B）\n" +
                    "iOS 仅保留 target 与接口，本阶段未验收。",
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
