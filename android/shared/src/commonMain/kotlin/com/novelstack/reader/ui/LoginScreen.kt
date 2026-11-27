package com.novelstack.reader.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import com.novelstack.reader.app.AppState

@Composable
fun LoginScreen(state: AppState) {
    val busy by state.busy.collectAsState()
    val error by state.error.collectAsState()

    // 第十阶段：不再硬编码 API 域名；优先用已保存的地址，否则留空由用户输入。
    var server by remember {
        mutableStateOf(
            com.novelstack.reader.net.ApiEndpointManager.primaryUrl()
                .ifBlank { com.novelstack.reader.net.ApiEndpointManager.activeBaseUrl() }
        )
    }
    var username by remember { mutableStateOf("admin") }
    var password by remember { mutableStateOf("") }
    var pairing by remember { mutableStateOf("") }
    var mode by remember { mutableIntStateOf(0) }
    var deviceName by remember { mutableStateOf("Android 阅读器") }

    Surface(modifier = Modifier.fillMaxSize()) {
        Column(
            modifier = Modifier
                .fillMaxSize()
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 28.dp, vertical = 48.dp),
            verticalArrangement = Arrangement.spacedBy(14.dp),
        ) {
            Text("NovelStack", style = MaterialTheme.typography.headlineMedium)
            Text(
                "私人书库 · 设备端解密",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(8.dp))

            OutlinedTextField(
                value = server,
                onValueChange = { server = it },
                label = { Text("服务端地址") },
                placeholder = { Text("http://10.0.2.2:8080") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = deviceName,
                onValueChange = { deviceName = it },
                label = { Text("设备名称") },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
            )

            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                TextButton(onClick = { mode = 0 }) {
                    Text(if (mode == 0) "• 用户名密码" else "用户名密码")
                }
                TextButton(onClick = { mode = 1 }) {
                    Text(if (mode == 1) "• 一次性配对码" else "一次性配对码")
                }
            }

            if (mode == 0) {
                OutlinedTextField(
                    value = username,
                    onValueChange = { username = it },
                    label = { Text("用户名") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                OutlinedTextField(
                    value = password,
                    onValueChange = { password = it },
                    label = { Text("密码") },
                    singleLine = true,
                    visualTransformation = PasswordVisualTransformation(),
                    keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                    modifier = Modifier.fillMaxWidth(),
                )
            } else {
                OutlinedTextField(
                    value = pairing,
                    onValueChange = { pairing = it },
                    label = { Text("配对码") },
                    placeholder = { Text("nsp_...") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                Text(
                    "在浏览器控制台 → 设置页生成，10 分钟内有效且只能使用一次。",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }

            Button(
                enabled = !busy,
                onClick = {
                    state.login(
                        server = server,
                        username = username,
                        password = password,
                        pairingCode = if (mode == 1) pairing else "",
                        deviceName = deviceName,
                    )
                },
                modifier = Modifier.fillMaxWidth(),
            ) {
                if (busy) {
                    CircularProgressIndicator(
                        modifier = Modifier.width(18.dp).height(18.dp),
                        strokeWidth = 2.dp,
                    )
                } else {
                    Text("配对并同步")
                }
            }

            error?.let {
                Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodyMedium)
            }

            Spacer(Modifier.height(12.dp))
            HorizontalDivider()
            Spacer(Modifier.height(12.dp))
            Text(
                "安全说明",
                style = MaterialTheme.typography.titleSmall,
            )
            Text(
                "• 设备私钥在 Android Keystore 内生成，不可导出；\n" +
                    "• 服务端只返回被设备公钥包装的书籍密钥与密文；\n" +
                    "• 正文在设备内解密后写入 SQLCipher，不进日志、不进备份；\n" +
                    "• 退出时提供「清除本地数据」。",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                fontFamily = FontFamily.Default,
            )
        }
    }
}
