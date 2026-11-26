package com.novelstack.reader.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Slider
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.ReaderSettings
import com.novelstack.reader.app.Screen

@Composable
fun ReaderScreen(state: AppState, bookId: String) {
    val r by state.reader.collectAsState()
    val settings by state.settings.collectAsState()
    val tts by state.tts.state.collectAsState()
    val palette = readerPalette(settings.background)

    var showToc by remember { mutableStateOf(false) }
    var showSettings by remember { mutableStateOf(false) }

    // 从搜索/其他入口进入时，如果当前状态不是这本书，主动打开。
    LaunchedEffect(bookId) {
        if (r.book?.id != bookId) state.openBook(bookId)
    }

    Box(modifier = Modifier.fillMaxSize().background(palette.bg)) {
        Column(modifier = Modifier.fillMaxSize()) {
            Row(
                modifier = Modifier.fillMaxWidth().padding(horizontal = 4.dp, vertical = 2.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                TextButton(onClick = { state.screen.value = Screen.Library }) {
                    Text("‹ 书库", color = palette.dim)
                }
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        r.book?.title ?: "",
                        color = palette.fg,
                        style = MaterialTheme.typography.titleSmall,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                    Text(
                        r.title.ifBlank { "第 ${r.index + 1} 章" },
                        color = palette.dim,
                        style = MaterialTheme.typography.labelSmall,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
                TextButton(onClick = { showToc = true }) { Text("目录", color = palette.dim) }
                TextButton(onClick = { showSettings = true }) { Text("Aa", color = palette.dim) }
            }

            if (settings.pager == "scroll") {
                ChapterBody(
                    state = state,
                    palette = palette,
                    settings = settings,
                    modifier = Modifier.weight(1f),
                )
            } else {
                val pageCount = maxOf(1, r.chapters.size)
                val pager = rememberPagerState(initialPage = r.index.coerceIn(0, pageCount - 1)) {
                    pageCount
                }
                LaunchedEffect(r.index) {
                    if (pager.currentPage != r.index) pager.scrollToPage(r.index)
                }
                LaunchedEffect(pager.currentPage) {
                    if (pager.currentPage != r.index) state.goChapter(pager.currentPage)
                }
                HorizontalPager(state = pager, modifier = Modifier.weight(1f)) { page ->
                    if (page == r.index) {
                        ChapterBody(
                            state = state,
                            palette = palette,
                            settings = settings,
                            modifier = Modifier.fillMaxSize(),
                        )
                    } else {
                        Column(
                            modifier = Modifier.fillMaxSize().padding(24.dp),
                            verticalArrangement = Arrangement.Center,
                            horizontalAlignment = Alignment.CenterHorizontally,
                        ) {
                            Text(
                                r.chapters.getOrNull(page)?.title ?: "第 ${page + 1} 章",
                                color = palette.dim,
                                style = MaterialTheme.typography.titleMedium,
                            )
                            Spacer(Modifier.height(8.dp))
                            Text("松手后加载", color = palette.dim, style = MaterialTheme.typography.bodySmall)
                        }
                    }
                }
            }

            if (tts.playing) {
                Row(
                    modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 2.dp),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text(
                        "朗读 ${tts.segment}/${tts.total}",
                        color = palette.dim,
                        style = MaterialTheme.typography.labelSmall,
                        modifier = Modifier.weight(1f),
                    )
                    TextButton(onClick = { if (tts.paused) state.tts.resume() else state.tts.pause() }) {
                        Text(if (tts.paused) "继续" else "暂停", color = palette.dim)
                    }
                    TextButton(onClick = { state.tts.stop() }) { Text("停止", color = palette.dim) }
                }
            }
            tts.error?.let {
                Text(
                    "朗读失败：$it",
                    color = Color(0xFFE08A8A),
                    style = MaterialTheme.typography.labelSmall,
                    modifier = Modifier.padding(horizontal = 16.dp),
                )
            }

            Column(modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 6.dp)) {
                LinearProgressIndicator(
                    progress = { r.percent },
                    modifier = Modifier.fillMaxWidth().height(2.dp),
                    color = palette.dim,
                    trackColor = palette.dim.copy(alpha = 0.2f),
                )
                Row(verticalAlignment = Alignment.CenterVertically) {
                    TextButton(onClick = { state.goChapter(r.index - 1) }, enabled = r.index > 0) {
                        Text("上一章", color = if (r.index > 0) palette.dim else palette.dim.copy(alpha = 0.4f))
                    }
                    TextButton(onClick = { state.prefetchAhead() }) {
                        Text("缓存 3 章", color = palette.dim)
                    }
                    TextButton(
                        onClick = {
                            if (tts.playing) state.tts.stop()
                            else state.tts.play(bookId, r.index, settings.ttsSpeed)
                        },
                    ) {
                        Text(if (tts.playing) "停止朗读" else "朗读", color = palette.dim)
                    }
                    Spacer(Modifier.weight(1f))
                    Text(
                        "${r.index + 1}/${r.chapters.size} · ${(r.percent * 100).toInt()}%" +
                            if (r.fromCache) " · 离线" else "",
                        color = palette.dim,
                        style = MaterialTheme.typography.labelSmall,
                    )
                    TextButton(
                        onClick = { state.goChapter(r.index + 1) },
                        enabled = r.index < r.chapters.size - 1,
                    ) {
                        Text(
                            "下一章",
                            color = if (r.index < r.chapters.size - 1) palette.dim
                            else palette.dim.copy(alpha = 0.4f),
                        )
                    }
                }
            }
        }
    }

    if (showToc) {
        AlertDialog(
            onDismissRequest = { showToc = false },
            title = { Text("目录（${r.chapters.size} 章）") },
            text = {
                LazyColumn(modifier = Modifier.heightIn(max = 420.dp)) {
                    items(r.chapters, key = { it.index }) { c ->
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .clickable {
                                    showToc = false
                                    state.goChapter(c.index)
                                }
                                .padding(vertical = 10.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            Text(
                                "${c.index + 1}",
                                style = MaterialTheme.typography.labelSmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                                modifier = Modifier.width(34.dp),
                            )
                            Text(
                                c.title.ifBlank { "第 ${c.index + 1} 章" },
                                style = MaterialTheme.typography.bodyMedium,
                                fontWeight = if (c.index == r.index) FontWeight.SemiBold else FontWeight.Normal,
                                modifier = Modifier.weight(1f),
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                            )
                            if (c.cached) {
                                Text("●", color = MaterialTheme.colorScheme.primary, fontSize = 10.sp)
                            }
                        }
                    }
                }
            },
            confirmButton = { TextButton(onClick = { showToc = false }) { Text("关闭") } },
        )
    }

    if (showSettings) {
        ReaderSettingsDialog(
            current = settings,
            onChange = { state.updateSettings(it) },
            onDismiss = { showSettings = false },
        )
    }
}

@Composable
private fun ChapterBody(
    state: AppState,
    palette: com.novelstack.reader.ui.ReaderPalette,
    settings: ReaderSettings,
    modifier: Modifier = Modifier,
) {
    val r by state.reader.collectAsState()
    val scroll = rememberScrollState()
    Column(
        modifier = modifier
            .fillMaxSize()
            .verticalScroll(scroll)
            .padding(horizontal = 22.dp, vertical = 12.dp),
    ) {
        if (r.loading) {
            Box(Modifier.fillMaxWidth().padding(40.dp), contentAlignment = Alignment.Center) {
                CircularProgressIndicator(color = palette.dim)
            }
        }
        Text(
            r.title,
            color = palette.fg,
            style = MaterialTheme.typography.titleMedium,
            fontWeight = FontWeight.SemiBold,
            modifier = Modifier.padding(bottom = 14.dp),
        )
        Text(
            r.text,
            color = palette.fg,
            fontSize = settings.fontSize.sp,
            lineHeight = (settings.fontSize * settings.lineHeight).sp,
        )
        r.message?.let {
            Spacer(Modifier.height(16.dp))
            Text(it, color = Color(0xFFE08A8A), style = MaterialTheme.typography.bodySmall)
        }
        Spacer(Modifier.height(60.dp))
    }
}

/** common 没有 String.format，手动保留一位小数。 */
private fun fmt1(v: Float): String {
    val n = (v * 10).toInt()
    return "${n / 10}.${kotlin.math.abs(n % 10)}"
}

@Composable
private fun ReaderSettingsDialog(
    current: ReaderSettings,
    onChange: (ReaderSettings) -> Unit,
    onDismiss: () -> Unit,
) {
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text("阅读设置") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(10.dp)) {
                Text("字号 ${current.fontSize.toInt()}sp", style = MaterialTheme.typography.labelMedium)
                Slider(
                    value = current.fontSize,
                    onValueChange = { onChange(current.copy(fontSize = it)) },
                    valueRange = 14f..28f,
                    steps = 13,
                )
                Text("行距 ${fmt1(current.lineHeight)}", style = MaterialTheme.typography.labelMedium)
                Slider(
                    value = current.lineHeight,
                    onValueChange = { onChange(current.copy(lineHeight = it)) },
                    valueRange = 1.3f..2.2f,
                    steps = 8,
                )
                Text("背景", style = MaterialTheme.typography.labelMedium)
                Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                    listOf("dark" to Color(0xFF0D1117), "paper" to Color(0xFFF7F2E8),
                        "sepia" to Color(0xFFEFE2CC), "mint" to Color(0xFFE4F0E8)).forEach { (key, color) ->
                        Box(
                            modifier = Modifier
                                .size(30.dp)
                                .clip(CircleShape)
                                .background(color)
                                .border(
                                    width = if (current.background == key) 3.dp else 1.dp,
                                    color = if (current.background == key) MaterialTheme.colorScheme.primary
                                    else MaterialTheme.colorScheme.outline,
                                    shape = CircleShape,
                                )
                                .clickable { onChange(current.copy(background = key)) },
                        )
                    }
                }
                Text("翻页", style = MaterialTheme.typography.labelMedium)
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    TextButton(onClick = { onChange(current.copy(pager = "slide")) }) {
                        Text(if (current.pager == "slide") "• 左右滑动" else "左右滑动")
                    }
                    TextButton(onClick = { onChange(current.copy(pager = "scroll")) }) {
                        Text(if (current.pager == "scroll") "• 上下滚动" else "上下滚动")
                    }
                }
                Text("朗读语速 ${fmt1(current.ttsSpeed)}x", style = MaterialTheme.typography.labelMedium)
                Slider(
                    value = current.ttsSpeed,
                    onValueChange = { onChange(current.copy(ttsSpeed = it)) },
                    valueRange = 0.5f..2.0f,
                    steps = 5,
                )
            }
        },
        confirmButton = { TextButton(onClick = onDismiss) { Text("完成") } },
    )
}
