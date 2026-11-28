package com.novelstack.reader.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.Screen
import com.novelstack.reader.ui.components.ApplePressable
import com.novelstack.reader.ui.components.EmptyArt
import com.novelstack.reader.ui.components.EmptyState
import com.novelstack.reader.ui.components.SquircleCard
import com.novelstack.reader.model.HistoryItem
import kotlinx.datetime.Clock
import kotlinx.datetime.Instant

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun HistoryScreen(state: AppState) {
    val items by state.history.collectAsState()
    LaunchedEffect(Unit) { state.loadHistory() }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("足迹") },
                actions = { TextButton(onClick = { state.loadHistory() }) { Text("刷新") } },
            )
        },
    ) { pad ->
        if (items.isEmpty()) {
            Box(Modifier.fillMaxSize().padding(pad), contentAlignment = Alignment.Center) {
                EmptyState(
                    art = EmptyArt.Clock,
                    title = "还没有阅读足迹",
                    desc = "打开一本书读几页，位置就会记在这里；换设备登录也能看到同一份记录。",
                    cta = "去书架挑一本",
                    onCta = { state.screen.value = Screen.Library },
                )
            }
        } else {
            LazyColumn(
                modifier = Modifier.fillMaxSize().padding(pad),
                contentPadding = PaddingValues(14.dp),
                verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                items(items, key = { it.bookId }) { it ->
                    HistoryCard(it) { state.openAt(it.bookId, it.chapterIndex) }
                }
            }
        }
    }
}

@Composable
private fun HistoryCard(item: HistoryItem, onClick: () -> Unit) {
    SquircleCard(modifier = Modifier.fillMaxWidth()) {
        ApplePressable(onClick = onClick, shape = Squircle.card()) {
        Row(modifier = Modifier.padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
            Box(
                modifier = Modifier
                    .size(width = 42.dp, height = 56.dp)
                    .clip(Squircle.control())
                    .background(MaterialTheme.colorScheme.primary.copy(alpha = 0.16f)),
                contentAlignment = Alignment.Center,
            ) {
                Text(
                    item.title.take(1),
                    style = MaterialTheme.typography.titleLarge,
                    color = MaterialTheme.colorScheme.primary,
                    fontWeight = FontWeight.SemiBold,
                )
            }
            Spacer(Modifier.width(12.dp))
            Column(modifier = Modifier.weight(1f)) {
                Text(
                    item.title.ifBlank { item.bookId },
                    style = MaterialTheme.typography.titleSmall,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Spacer(Modifier.height(2.dp))
                Text(
                    item.chapterTitle.ifBlank { "第 ${item.chapterIndex + 1} 章" },
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Spacer(Modifier.height(6.dp))
                LinearProgressIndicator(
                    progress = { item.percent.toFloat().coerceIn(0f, 1f) },
                    modifier = Modifier.fillMaxWidth().height(3.dp),
                )
                Spacer(Modifier.height(4.dp))
                Row {
                    Text(
                        "${(item.percent * 100).toInt()}%",
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    Spacer(Modifier.weight(1f))
                    Text(
                        relativeTime(item.updatedAt),
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
        }
        }
    }
}

/** 把 RFC3339 时间转成「刚刚 / N 分钟前 / N 小时前 / N 天前」，解析失败则截取日期。 */
private fun relativeTime(iso: String): String {
    return try {
        val instant = Instant.parse(iso)
        val sec = (Clock.System.now() - instant).inWholeSeconds
        when {
            sec < 60 -> "刚刚"
            sec < 3600 -> "${sec / 60} 分钟前"
            sec < 86_400 -> "${sec / 3600} 小时前"
            sec < 86_400L * 30 -> "${sec / 86_400} 天前"
            else -> iso.take(10)
        }
    } catch (_: Throwable) {
        iso.take(10)
    }
}
