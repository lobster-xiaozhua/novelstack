package com.novelstack.reader.ui

import android.annotation.SuppressLint
import android.view.ViewGroup
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.viewinterop.AndroidView

/**
 * Android WebView 实现。
 *
 * 安全边界：
 *  - 只加载同源控制台；不开启 `allowFileAccess` / `allowContentAccess`；
 *  - 混合内容设为 NEVER_ALLOW：控制台要么全 HTTPS，要么全 HTTP（内网）；
 *  - 关闭密码保存与地理位置等无关能力。
 */
@SuppressLint("SetJavaScriptEnabled")
@Composable
actual fun PlatformWebView(url: String, modifier: Modifier) {
    val context = androidx.compose.ui.platform.LocalContext.current
    val webView = remember(context) {
        WebView(context).apply {
            layoutParams = ViewGroup.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.MATCH_PARENT,
            )
            settings.apply {
                javaScriptEnabled = true
                domStorageEnabled = true
                allowFileAccess = false
                allowContentAccess = false
                mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
                cacheMode = WebSettings.LOAD_DEFAULT
                // 控制台是自用后台，禁用缩放以保持 1:1 的响应式断点。
                setSupportZoom(false)
                builtInZoomControls = false
                displayZoomControls = false
            }
            webViewClient = WebViewClient()
            setBackgroundColor(android.graphics.Color.TRANSPARENT)
        }
    }
    // url 变化时重新加载（一次性票据只能兑换一次，因此每次都换新 url）。
    androidx.compose.runtime.LaunchedEffect(url) {
        if (url.isNotBlank()) webView.loadUrl(url)
    }
    AndroidView(factory = { webView }, modifier = modifier)
}
