package com.novelstack.reader.ui

import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight

/**
 * 平台 WebView：Android 用系统 WebView，其它平台给占位。
 *
 * 只用于打开本站控制台（同源、已带一次性票据），因此允许 JS 与 DOM Storage，
 * 但不开启文件访问、不允许混合内容。
 */
@Composable
expect fun PlatformWebView(url: String, modifier: Modifier = Modifier)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun WebConsoleScreen(url: String, onClose: () -> Unit) {
    Scaffold(
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            TopAppBar(
                title = { Text("控制台", fontWeight = FontWeight.SemiBold) },
                navigationIcon = { TextButton(onClick = onClose) { Text("返回") } },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.background,
                ),
            )
        },
    ) { pad ->
        PlatformWebView(url = url, modifier = Modifier.fillMaxSize().padding(pad))
    }
}
