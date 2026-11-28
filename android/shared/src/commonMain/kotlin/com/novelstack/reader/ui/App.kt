package com.novelstack.reader.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.clickable
import androidx.compose.ui.draw.blur
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.NetworkStatus
import com.novelstack.reader.app.Screen

@OptIn(androidx.compose.material3.ExperimentalMaterial3Api::class)
@Composable
fun NovelApp(state: AppState) {
    val screen by state.screen.collectAsState()
    val settings by state.settings.collectAsState()
    val systemDark = isSystemInDarkTheme()
    val locked by state.locked.collectAsState()
    val onboarded by state.onboarded.collectAsState()

    // 终端防御：阅读/设置页防截屏（FLAG_SECURE）随页面切换。
    androidx.compose.runtime.LaunchedEffect(screen) {
        state.updateSecureFlag(screen)
    }

    NovelTheme(dark = isDarkTheme(settings.theme, systemDark)) {
        // 全局关闭 Material 的方形水波纹：交互反馈统一由 ApplePressable 的
        // 缩放 + 浅蓝叠加承担（见 components/AppleUI.kt）。
        androidx.compose.runtime.CompositionLocalProvider(
            androidx.compose.material3.LocalRippleConfiguration provides null,
        ) {
        Box(modifier = Modifier.fillMaxSize()) {
            when (val s = screen) {
                Screen.Login -> LoginScreen(state)
                Screen.Search -> SearchScreen(state)
                Screen.ServerSettings -> ServerSettingsScreen(state)
                Screen.Guide -> GuideScreen(state) { state.screen.value = Screen.Settings }
                is Screen.Reader -> ReaderScreen(state, s.bookId)
                is Screen.WebConsole -> WebConsoleScreen(s.url) { state.screen.value = Screen.Settings }
                else -> MainTabs(state, s)
            }
            // 首次启动：盖一层欢迎页（登录页不盖——还没配对时看引导没有意义）。
            if (!onboarded && screen !is Screen.Login) {
                OnboardingScreen(onDone = { state.completeOnboarding() })
            }
            // 应用锁：验证未通过时全屏模糊遮罩，内容不可见也不可操作。
            if (locked) {
                AppLockOverlay(state)
            }
        }
        }
    }
}

/**
 * 生物识别遮罩：底层内容加高斯模糊并叠半透明层，点击遮罩重新发起验证。
 * 失败时显示错误文案并提供「重试」，用户必须通过才能查阅书架与正文。
 */
@Composable
private fun AppLockOverlay(state: AppState) {
    var errorText by androidx.compose.runtime.remember { androidx.compose.runtime.mutableStateOf<String?>(null) }
    Box(
        modifier = Modifier
            .fillMaxSize()
            // 内容整体模糊，遮住书架/正文；半径较大确保文字不可读。
            .blur(28.dp)
            .background(MaterialTheme.colorScheme.surface.copy(alpha = 0.82f))
            .clickable {
                state.unlockWithBiometric { _, msg ->
                    errorText = msg.ifBlank { "验证失败，请重试" }
                }
            },
        contentAlignment = androidx.compose.ui.Alignment.Center,
    ) {
        Column(
            horizontalAlignment = androidx.compose.ui.Alignment.CenterHorizontally,
            verticalArrangement = androidx.compose.foundation.layout.Arrangement.spacedBy(12.dp),
            modifier = Modifier.padding(24.dp),
        ) {
            Text("🔒", style = MaterialTheme.typography.displayMedium)
            Text("应用已锁定", style = MaterialTheme.typography.titleLarge, color = MaterialTheme.colorScheme.onSurface)
            Text(
                "点击屏幕使用指纹 / 面容 / 设备密码解锁",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            errorText?.let {
                Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
            }
            androidx.compose.material3.Button(onClick = {
                state.unlockWithBiometric { _, msg ->
                    errorText = msg.ifBlank { "验证失败，请重试" }
                }
            }) {
                Text(if (errorText == null) "解锁" else "重试")
            }
        }
    }
    // 首次进入自动拉起一次验证，避免用户还要多点一下。
    androidx.compose.runtime.LaunchedEffect(Unit) {
        state.unlockWithBiometric { _, msg ->
            errorText = msg.ifBlank { "验证失败，请重试" }
        }
    }
}

/** 四个主 Tab：书架 / 发现 / 足迹 / 设置。阅读页全屏，不带底栏。 */
@Composable
private fun MainTabs(state: AppState, current: Screen) {
    val network by state.network.collectAsState()
    val offline by state.offline.collectAsState()
    Column(modifier = Modifier.fillMaxSize().statusBarsPadding()) {
        NetworkBanner(network)
        if (offline && network != NetworkStatus.Offline) {
            OfflineBanner(onRetry = { state.retryConnection() })
        }
        Box(modifier = Modifier.weight(1f)) {
            when (current) {
                Screen.Library -> LibraryScreen(state)
                Screen.Discover -> DiscoverScreen(state)
                Screen.History -> HistoryScreen(state)
                Screen.Settings -> SettingsScreen(state)
                Screen.Stats -> StatsScreen(state)
                else -> LibraryScreen(state)
            }
        }
        val tabs = listOf(
            Triple(Screen.Library, "书架", "▤"),
            Triple(Screen.Discover, "发现", "◈"),
            Triple(Screen.History, "足迹", "◷"),
            Triple(Screen.Settings, "设置", "⚙"),
        )
        androidx.compose.material3.HorizontalDivider(
            thickness = AppleTokens.HAIRLINE_DP.dp,
            color = MaterialTheme.colorScheme.outlineVariant,
        )
        NavigationBar(
            containerColor = MaterialTheme.colorScheme.surface,
            tonalElevation = 0.dp,
        ) {
            tabs.forEach { (target, label, glyph) ->
                NavigationBarItem(
                    selected = current == target,
                    onClick = { state.screen.value = target },
                    icon = { Text(glyph, style = MaterialTheme.typography.titleMedium) },
                    label = { Text(label, style = MaterialTheme.typography.labelSmall) },
                    colors = androidx.compose.material3.NavigationBarItemDefaults.colors(
                        selectedIconColor = MaterialTheme.colorScheme.primary,
                        selectedTextColor = MaterialTheme.colorScheme.primary,
                        indicatorColor = AppleTokens.SelectionBg,
                        unselectedIconColor = MaterialTheme.colorScheme.onSurfaceVariant,
                        unselectedTextColor = MaterialTheme.colorScheme.onSurfaceVariant,
                    ),
                )
            }
        }
    }
}

/**
 * 离线熔断横幅：连续 3 次 API 超时后出现。
 * 与「系统断网」不同——这里网卡是通的，只是服务端不可达（或极慢）。
 */
@Composable
private fun OfflineBanner(onRetry: () -> Unit) {
    Row(
        modifier = Modifier
            .fillMaxWidth()
            .background(MaterialTheme.colorScheme.errorContainer)
            .padding(horizontal = 12.dp, vertical = 6.dp),
        verticalAlignment = androidx.compose.ui.Alignment.CenterVertically,
    ) {
        Text(
            "服务器连接失败，已进入离线模式",
            color = MaterialTheme.colorScheme.onErrorContainer,
            style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.weight(1f),
        )
        Text(
            "重试",
            color = MaterialTheme.colorScheme.onErrorContainer,
            style = MaterialTheme.typography.labelMedium,
            modifier = Modifier
                .padding(start = 8.dp)
                .clickable(onClick = onRetry),
        )
    }
}

/**
 * 网络横幅：只在断网/移动网络时出现。
 * 断网=红色提示，移动网络=中性提示（告诉用户预读已暂停，不是出错）。
 */
@Composable
private fun NetworkBanner(status: NetworkStatus) {
    if (status == NetworkStatus.Online) return
    val bg = if (status == NetworkStatus.Offline) {
        MaterialTheme.colorScheme.errorContainer
    } else {
        MaterialTheme.colorScheme.secondaryContainer
    }
    val fg = if (status == NetworkStatus.Offline) {
        MaterialTheme.colorScheme.onErrorContainer
    } else {
        MaterialTheme.colorScheme.onSecondaryContainer
    }
    Text(
        text = if (status == NetworkStatus.Offline) {
            "网络已断开 · 预读与同步已暂停，可继续阅读已缓存章节"
        } else {
            "移动网络 · 预读与遥测上传已暂停以节省流量"
        },
        color = fg,
        style = MaterialTheme.typography.labelMedium,
        textAlign = TextAlign.Center,
        modifier = Modifier
            .fillMaxWidth()
            .background(bg)
            .padding(horizontal = 12.dp, vertical = 7.dp),
    )
}
