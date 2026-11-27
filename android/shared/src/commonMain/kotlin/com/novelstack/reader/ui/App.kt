package com.novelstack.reader.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
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
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.NetworkStatus
import com.novelstack.reader.app.Screen

@Composable
fun NovelApp(state: AppState) {
    val screen by state.screen.collectAsState()
    val settings by state.settings.collectAsState()
    val systemDark = isSystemInDarkTheme()
    NovelTheme(dark = isDarkTheme(settings.theme, systemDark)) {
        when (val s = screen) {
            Screen.Login -> LoginScreen(state)
            Screen.Search -> SearchScreen(state)
            is Screen.Reader -> ReaderScreen(state, s.bookId)
            else -> MainTabs(state, s)
        }
    }
}

/** 四个主 Tab：书架 / 发现 / 足迹 / 设置。阅读页全屏，不带底栏。 */
@Composable
private fun MainTabs(state: AppState, current: Screen) {
    val network by state.network.collectAsState()
    Column(modifier = Modifier.fillMaxSize().statusBarsPadding()) {
        NetworkBanner(network)
        Box(modifier = Modifier.weight(1f)) {
            when (current) {
                Screen.Library -> LibraryScreen(state)
                Screen.Discover -> DiscoverScreen(state)
                Screen.History -> HistoryScreen(state)
                Screen.Settings -> SettingsScreen(state)
                else -> LibraryScreen(state)
            }
        }
        val tabs = listOf(
            Triple(Screen.Library, "书架", "▤"),
            Triple(Screen.Discover, "发现", "◈"),
            Triple(Screen.History, "足迹", "◷"),
            Triple(Screen.Settings, "设置", "⚙"),
        )
        NavigationBar {
            tabs.forEach { (target, label, glyph) ->
                NavigationBarItem(
                    selected = current == target,
                    onClick = { state.screen.value = target },
                    icon = { Text(glyph, style = MaterialTheme.typography.titleMedium) },
                    label = { Text(label) },
                )
            }
        }
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
