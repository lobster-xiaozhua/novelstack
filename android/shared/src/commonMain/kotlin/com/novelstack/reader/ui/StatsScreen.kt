package com.novelstack.reader.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Card
import androidx.compose.material3.ExperimentalMaterial3Api
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
import androidx.compose.ui.unit.dp
import com.novelstack.reader.app.AppState
import com.novelstack.reader.ui.components.SquircleCard
import com.novelstack.reader.app.Screen
import com.novelstack.reader.model.ReadingDay
import com.novelstack.reader.model.ReadingStats
import kotlinx.datetime.LocalDate

/**
 * 阅读统计：今日/累计/连续天数 + 最近 30 天柱状图 + 90 天日历热力图。
 *
 * 数据来源 GET /api/stats/reading；日期口径是服务端时区，客户端不做二次换算，
 * 避免设备时区与服务端不一致时出现「今天读到昨天」的错位。
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun StatsScreen(state: AppState) {
    val stats by state.readingStats.collectAsState()
    val offline by state.offline.collectAsState()

    LaunchedEffect(Unit) {
        if (stats == null) state.loadReadingStats()
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("阅读统计") },
                navigationIcon = {
                    TextButton(onClick = { state.screen.value = Screen.Settings }) { Text("‹ 设置") }
                },
                actions = {
                    TextButton(onClick = { state.loadReadingStats() }) { Text("刷新") }
                },
            )
        },
    ) { pad ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(pad)
                .verticalScroll(rememberScrollState())
                .padding(16.dp),
        ) {
            if (offline) {
                Text(
                    "离线模式：显示的是上次同步的数据",
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.error,
                )
                Spacer(Modifier.height(8.dp))
            }
            val s = stats
            if (s == null) {
                Text(
                    "暂无数据（需要联网同步一次）",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                return@Column
            }

            KpiRow(s)
            Spacer(Modifier.height(18.dp))
            BarChart(s.days.takeLast(30))
            Spacer(Modifier.height(18.dp))
            Heatmap(s.days)
            Spacer(Modifier.height(14.dp))
            Text(
                "统计按服务端自然日累计，只记录时长与天数，不包含书名与正文。",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

@Composable
private fun KpiRow(s: ReadingStats) {
    Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        KpiCard("今日", humanDuration(s.todaySeconds), Modifier.weight(1f))
        KpiCard("累计", humanDuration(s.totalSeconds), Modifier.weight(1f))
        KpiCard("连续", "${s.streakDays} 天", Modifier.weight(1f))
        KpiCard("最长", "${s.bestStreak} 天", Modifier.weight(1f))
    }
}

@Composable
private fun KpiCard(label: String, value: String, modifier: Modifier = Modifier) {
    SquircleCard(modifier = modifier) {
        Column(modifier = Modifier.padding(horizontal = 10.dp, vertical = 12.dp)) {
            Text(
                label,
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(4.dp))
            Text(
                value,
                style = MaterialTheme.typography.titleMedium,
                fontWeight = FontWeight.SemiBold,
                maxLines = 1,
            )
        }
    }
}

/** 30 天柱状图：柱高 = 当天秒数 / 区间最大值。 */
@Composable
private fun BarChart(days: List<ReadingDay>) {
    if (days.isEmpty()) return
    val max = maxOf(1L, days.maxOf { it.seconds })
    Card {
        Column(modifier = Modifier.padding(14.dp)) {
            Text("最近 30 天", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
            Spacer(Modifier.height(12.dp))
            Row(
                modifier = Modifier.fillMaxWidth().height(120.dp),
                horizontalArrangement = Arrangement.spacedBy(3.dp),
                verticalAlignment = Alignment.Bottom,
            ) {
                days.forEach { d ->
                    val ratio = (d.seconds.toFloat() / max).coerceIn(0f, 1f)
                    Box(
                        modifier = Modifier
                            .weight(1f)
                            .height((2 + ratio * 118).dp)
                            .clip(RoundedCornerShape(topStart = 2.dp, topEnd = 2.dp))
                            .background(
                                if (d.seconds == 0L) {
                                    MaterialTheme.colorScheme.surfaceVariant
                                } else {
                                    MaterialTheme.colorScheme.primary.copy(alpha = 0.35f + 0.65f * ratio)
                                },
                            ),
                    )
                }
            }
            Spacer(Modifier.height(6.dp))
            Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                Text(
                    shortDate(days.first().day),
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Text(
                    shortDate(days.last().day),
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
    }
}

/**
 * 90 天热力图：列 = 周，行 = 周一..周日。
 * 第一列前用空白补齐，保证同一行始终是星期几。
 */
@Composable
private fun Heatmap(days: List<ReadingDay>) {
    if (days.isEmpty()) return
    val first = runCatching { LocalDate.parse(days.first().day) }.getOrNull() ?: return
    // kotlinx-datetime: DayOfWeek.MONDAY.ordinal == 0
    val pad = first.dayOfWeek.ordinal
    val cells = List<ReadingDay?>(pad) { null } + days
    val cols = cells.chunked(7)
    val max = maxOf(1L, days.maxOf { it.seconds })

    Card {
        Column(modifier = Modifier.padding(14.dp)) {
            Text("阅读日历", style = MaterialTheme.typography.titleSmall, fontWeight = FontWeight.SemiBold)
            Text(
                "最近 90 天 · 颜色越深读得越久",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(10.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(3.dp)) {
                Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
                    listOf("一", "二", "三", "四", "五", "六", "日").forEach {
                        Text(
                            it,
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            modifier = Modifier.size(12.dp),
                        )
                    }
                }
                Row(horizontalArrangement = Arrangement.spacedBy(3.dp)) {
                    cols.forEach { col ->
                        Column(verticalArrangement = Arrangement.spacedBy(3.dp)) {
                            repeat(7) { i ->
                                val cell = col.getOrNull(i)
                                Box(
                                    modifier = Modifier
                                        .size(12.dp)
                                        .clip(RoundedCornerShape(2.dp))
                                        .background(heatColor(cell, max)),
                                )
                            }
                        }
                    }
                }
            }
            Spacer(Modifier.height(8.dp))
            Text(
                "共 ${days.count { it.seconds > 0 }} 天有记录",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

@Composable
private fun heatColor(cell: ReadingDay?, max: Long) = when {
    cell == null || cell.seconds <= 0L -> MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.45f)
    else -> {
        val r = (cell.seconds.toFloat() / max).coerceIn(0f, 1f)
        MaterialTheme.colorScheme.primary.copy(alpha = 0.22f + 0.78f * r)
    }
}

/** 秒 → 人类可读（common 代码里没有 String.format，手写）。 */
private fun humanDuration(seconds: Long): String {
    if (seconds <= 0) return "0 分"
    val totalMinutes = seconds / 60
    if (totalMinutes < 60) return "$totalMinutes 分"
    val h = totalMinutes / 60
    val m = totalMinutes % 60
    return if (m == 0L) "$h 时" else "$h 时$m 分"
}

private fun shortDate(day: String): String {
    val parts = day.split("-")
    if (parts.size < 3) return day
    val m = parts[1].trimStart('0').ifEmpty { "0" }
    val d = parts[2].trimStart('0').ifEmpty { "0" }
    return "$m/$d"
}
