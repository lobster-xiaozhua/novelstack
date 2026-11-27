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
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.Screen
import com.novelstack.reader.model.LanService

/**
 * 第十阶段：服务器设置页。
 *
 * 能力：
 *  1. 手动修改公网主地址 / 内网地址（双节点降级）；
 *  2. 从剪贴板导入 JSON/Base64 配置，一键切换服务器；
 *  3. NsdManager + UDP 广播发现局域网服务；
 *  4. 扫二维码 / 输入 6 位码完成局域网配对。
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ServerSettingsScreen(state: AppState) {
    val server by state.serverUrl.collectAsState()
    val message by state.endpointMessage.collectAsState()
    val busy by state.busy.collectAsState()
    val services by state.lanServices.collectAsState()
    val usingLocal by state.usingLocalEndpoint.collectAsState()

    var primaryInput by remember { mutableStateOf(server) }
    var localInput by remember { mutableStateOf("") }
    var importInput by remember { mutableStateOf("") }
    var pairCode by remember { mutableStateOf("") }
    var deviceName by remember { mutableStateOf("Android 阅读器") }
    var discovering by remember { mutableStateOf(false) }
    var selected by remember { mutableStateOf<LanService?>(null) }
    var showImport by remember { mutableStateOf(false) }

    // 进入页面时刷新一次当前节点状态。
    LaunchedEffect(Unit) {
        state.refreshEndpointState()
        primaryInput = state.currentEndpoint()
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("服务器设置") },
                navigationIcon = {
                    TextButton(onClick = { state.screen.value = Screen.Settings }) { Text("返回") }
                },
            )
        },
    ) { pad ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(pad)
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 18.dp, vertical = 12.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text("动态根地址", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.SemiBold)
            Text(
                "客户端不硬编码任何 API 域名。当前生效节点：${if (usingLocal) "内网（优先）" else "公网"}",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Text(
                "当前地址：${state.currentEndpoint()}",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.primary,
            )

            OutlinedTextField(
                value = primaryInput,
                onValueChange = { primaryInput = it },
                label = { Text("公网主地址 (primary_url)") },
                placeholder = { Text("https://api.example.com") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = localInput,
                onValueChange = { localInput = it },
                label = { Text("内网地址 (local_url，可选)") },
                placeholder = { Text("http://192.168.1.100:8443") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                modifier = Modifier.fillMaxWidth(),
            )
            Text(
                "发起请求前会优先探测内网 /healthz（500ms），不通则自动降级到公网。" +
                    "探测结果缓存 10 秒，避免每次请求都等待。",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(onClick = { state.setPrimaryServer(primaryInput) }) { Text("保存主地址") }
                TextButton(onClick = { state.setLocalServer(localInput) }) { Text("保存内网地址") }
            }

            HorizontalDivider(Modifier.padding(vertical = 4.dp))
            Text("从剪贴板导入", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
            Text(
                "支持 JSON 或 Base64(JSON)：含 url / local_url / token 字段，一键切换服务器与凭证。",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                TextButton(onClick = { showImport = true }) { Text("粘贴并导入") }
                TextButton(onClick = {
                    importInput = state.exportEndpoint()
                    showImport = true
                }) { Text("导出当前配置") }
            }

            HorizontalDivider(Modifier.padding(vertical = 4.dp))
            Text("局域网发现与配对", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
            Text(
                "同一 WiFi 下自动发现运行中的服务端（mDNS _novelstack._tcp + UDP 广播）。",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(onClick = {
                    discovering = !discovering
                    if (discovering) state.startLanDiscovery() else state.stopLanDiscovery()
                }) {
                    Text(if (discovering) "停止扫描" else "扫描局域网")
                }
                TextButton(onClick = { state.scanQrToPair(deviceName) }) { Text("扫配对二维码") }
            }

            if (services.isNotEmpty()) {
                Text("发现 ${services.size} 台服务端：", style = MaterialTheme.typography.labelMedium)
                services.forEach { svc ->
                    TextButton(
                        onClick = { selected = svc },
                        modifier = Modifier.fillMaxWidth(),
                    ) {
                        Column {
                            Text("${svc.name}  ${svc.host}:${svc.port}")
                            Text(svc.endpoint, style = MaterialTheme.typography.labelSmall)
                        }
                    }
                }
            } else if (discovering) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    CircularProgressIndicator(modifier = Modifier.height(16.dp).width(16.dp), strokeWidth = 2.dp)
                    Spacer(Modifier.width(8.dp))
                    Text("正在扫描局域网…", style = MaterialTheme.typography.bodySmall)
                }
            }

            OutlinedTextField(
                value = deviceName,
                onValueChange = { deviceName = it },
                label = { Text("设备名称") },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = pairCode,
                onValueChange = { pairCode = it },
                label = { Text("6 位配对码") },
                placeholder = { Text("控制台生成的 6 位数字") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Number),
                modifier = Modifier.fillMaxWidth(),
            )
            Button(
                enabled = !busy && pairCode.length == 6,
                onClick = {
                    val target = selected
                    if (target == null) {
                        state.endpointMessage.value = "请先扫描并选择一台局域网服务端"
                    } else {
                        state.pairLanService(target, pairCode, deviceName)
                    }
                },
                modifier = Modifier.fillMaxWidth(),
            ) {
                if (busy) CircularProgressIndicator(modifier = Modifier.height(18.dp).width(18.dp), strokeWidth = 2.dp)
                else Text("用 6 位码配对")
            }

            message?.let {
                Text(it, color = MaterialTheme.colorScheme.primary, style = MaterialTheme.typography.bodyMedium)
            }
            Spacer(Modifier.height(16.dp))
        }
    }

    if (showImport) {
        AlertDialog(
            onDismissRequest = { showImport = false },
            title = { Text("导入服务器配置") },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                    Text(
                        "粘贴从其它设备导出的 Base64 文本，或包含 url/token 的 JSON。",
                        style = MaterialTheme.typography.bodySmall,
                    )
                    OutlinedTextField(
                        value = importInput,
                        onValueChange = { importInput = it },
                        label = { Text("配置文本") },
                        modifier = Modifier.fillMaxWidth(),
                        maxLines = 4,
                    )
                }
            },
            confirmButton = {
                TextButton(onClick = {
                    state.importEndpoint(importInput)
                    showImport = false
                }) { Text("导入并切换") }
            },
            dismissButton = { TextButton(onClick = { showImport = false }) { Text("取消") } },
        )
    }
}
