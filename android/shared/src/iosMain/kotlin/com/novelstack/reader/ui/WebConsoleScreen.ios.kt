package com.novelstack.reader.ui

import androidx.compose.foundation.layout.Box
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier

/** iOS 暂不内嵌控制台：WebView 需 UIKit 包装，第五阶段不验收 iOS 运行。 */
@Composable
actual fun PlatformWebView(url: String, modifier: Modifier) {
    Box(modifier = modifier, contentAlignment = Alignment.Center) {
        Text("iOS 端暂不支持内嵌控制台", color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}
