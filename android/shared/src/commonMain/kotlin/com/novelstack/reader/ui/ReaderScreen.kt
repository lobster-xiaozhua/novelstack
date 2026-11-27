package com.novelstack.reader.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.gestures.drag
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.displayCutout
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.windowInsetsPadding
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
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.input.pointer.positionChange
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.LineBreak
import androidx.compose.ui.text.style.TextIndent
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.ReaderSettings
import com.novelstack.reader.app.Screen
import com.novelstack.reader.reading.Kinsoku
import com.novelstack.reader.reading.PrefetchGate
import com.novelstack.reader.tts.SleepMode
import com.novelstack.reader.tts.TtsController

@Composable
fun ReaderScreen(state: AppState, bookId: String) {
    val r by state.reader.collectAsState()
    val settings by state.settings.collectAsState()
    val tts by state.tts.state.collectAsState()
    val books by state.books.collectAsState()
    val systemDark = isSystemInDarkTheme()
    val palette = readerPaletteFor(settings.theme, systemDark)
    val favorite = books.firstOrNull { it.id == bookId }?.is_favorite != 0L
    val offline by state.offline.collectAsState()
    val prompt by state.offlinePrompt.collectAsState()

    var showToc by remember { mutableStateOf(false) }
    var showSettings by remember { mutableStateOf(false) }
    // 亮度本地状态：左右边缘上下滑时更新，交由平台层写入 Window。
    var brightness by remember { mutableFloatStateOf(0.7f) }

    LaunchedEffect(bookId) {
        if (r.book?.id != bookId) state.openBook(bookId)
    }

    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(palette.bg)
            // 第九阶段：默认避开状态栏与挖孔；开启「隐藏刘海」后铺满，换取更宽的行宽。
            .then(
                if (settings.hideNotch) {
                    Modifier
                } else {
                    Modifier.windowInsetsPadding(WindowInsets.displayCutout).statusBarsPadding()
                },
            )
            // 第八阶段：左右两侧 30% 区域内上下滑动调节亮度；中间区域不拦截，保持滚动。
            .pointerInput(Unit) {
                awaitEachGesture {
                    val down = awaitFirstDown(requireUnconsumed = false)
                    val w = size.width.toFloat()
                    val x = down.position.x
                    val edge = x < w * 0.3f || x > w * 0.7f
                    if (!edge) return@awaitEachGesture
                    drag(down.id) { change ->
                        val dy = change.positionChange().y
                        brightness = (brightness - dy / size.height * 0.6f).coerceIn(0.05f, 1f)
                        state.setScreenBrightness(brightness)
                        change.consume()
                    }
                }
            },
    ) {
        Column(modifier = Modifier.fillMaxSize()) {
            Row(
                modifier = Modifier.fillMaxWidth().padding(horizontal = 4.dp, vertical = 2.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                TextButton(onClick = { state.screen.value = Screen.Library }) {
                    Text("‹ 书架", color = palette.dim)
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
                TextButton(onClick = { state.toggleFavorite(bookId) }) {
                    Text(if (favorite) "♥" else "♡", color = if (favorite) Color(0xFFE08A8A) else palette.dim)
                }
                TextButton(onClick = { showToc = true }) { Text("目录", color = palette.dim) }
                TextButton(onClick = { showSettings = true }) { Text("Aa", color = palette.dim) }
            }

            if (settings.pager == "scroll") {
                ChapterBody(state, palette, settings, Modifier.weight(1f))
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
                    // 覆盖模式：被翻走的页轻微后退形成层次；平移模式保持默认跟手。
                    val offset = (pager.currentPage - page) + pager.currentPageOffsetFraction
                    val pageMod = if (settings.pager == "cover") {
                        Modifier.graphicsLayer { translationX = -offset * size.width * 0.18f }
                    } else {
                        Modifier
                    }
                    Box(pageMod) {
                        if (page == r.index) {
                            ChapterBody(state, palette, settings, Modifier.fillMaxSize())
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
            }

            if (tts.playing) {
                PlaybackPanel(state = state, tts = tts, palette = palette)
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
                            else state.ttsStart(bookId, r.index, settings.ttsSpeed)
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
                        enabled = r.index < r.chapters.size - 1 &&
                            (!offline || r.chapters.getOrNull(r.index + 1)?.cached == true),
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

    val p = prompt
    if (p != null && p.bookId == bookId) {
        AlertDialog(
            onDismissRequest = { state.resolveOfflinePrompt(useLocal = true) },
            title = { Text("检测到离线进度") },
            text = {
                Text(
                    "本机停在第 ${p.localChapter + 1} 章（共 ${p.totalChapters} 章），" +
                        "云端记录在第 ${p.remoteChapter + 1} 章。\n\n" +
                        "离线期间读得更远，是否跳转到本地进度？",
                )
            },
            confirmButton = {
                TextButton(onClick = { state.resolveOfflinePrompt(useLocal = true) }) {
                    Text("跳到第 ${p.localChapter + 1} 章")
                }
            },
            dismissButton = {
                TextButton(onClick = { state.resolveOfflinePrompt(useLocal = false) }) {
                    Text("留在云端进度")
                }
            },
        )
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
                                // 当前章已在内存里，即使 cached 标记还没刷新也允许点击。
                                .clickable(enabled = !offline || c.cached || c.index == r.index) {
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
                                color = if (offline && !c.cached) {
                                    MaterialTheme.colorScheme.onSurfaceVariant.copy(alpha = 0.4f)
                                } else {
                                    MaterialTheme.colorScheme.onSurface
                                },
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
    palette: ReaderPalette,
    settings: ReaderSettings,
    modifier: Modifier = Modifier,
) {
    val r by state.reader.collectAsState()
    val scroll = rememberScrollState()
    val gate = remember { PrefetchGate(0.8f) }

    // 第八阶段：滚动进度超过 80% 时，静默预拉下一章（每章仅触发一次）。
    LaunchedEffect(scroll.value, scroll.maxValue, r.index) {
        if (scroll.maxValue > 0) {
            val progress = scroll.value.toFloat() / scroll.maxValue
            val hasNext = r.index < r.chapters.size - 1
            if (gate.shouldTrigger(r.index, progress, hasNext)) {
                state.prefetchNextChapter()
            }
        }
    }

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
            // 避头尾近似：在禁则标点前插入 word joiner；首行缩进 2 字符；段落断行策略。
            Kinsoku.apply(r.text),
            color = palette.fg,
            style = MaterialTheme.typography.bodyLarge.copy(
                fontSize = settings.fontSize.sp,
                lineHeight = (settings.fontSize * settings.lineHeight).sp,
                textIndent = TextIndent(firstLine = (settings.fontSize * 2).sp),
                lineBreak = LineBreak.Paragraph,
            ),
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
                Text("主题", style = MaterialTheme.typography.labelMedium)
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    listOf(
                        "auto" to "跟随系统",
                        "light" to "白天",
                        "dark" to "夜间",
                        "eye" to "护眼",
                    ).forEach { (key, label) ->
                        TextButton(onClick = { onChange(current.copy(theme = key)) }) {
                            Text(
                                (if (current.theme == key) "• " else "") + label,
                                style = MaterialTheme.typography.labelMedium,
                            )
                        }
                    }
                }
                Text("翻页", style = MaterialTheme.typography.labelMedium)
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    listOf(
                        "slide" to "平移",
                        "cover" to "覆盖",
                        "scroll" to "滚动",
                    ).forEach { (key, label) ->
                        TextButton(onClick = { onChange(current.copy(pager = key)) }) {
                            Text(
                                (if (current.pager == key) "• " else "") + label,
                                style = MaterialTheme.typography.labelMedium,
                            )
                        }
                    }
                }
                Text("屏幕", style = MaterialTheme.typography.labelMedium)
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                    TextButton(onClick = { onChange(current.copy(hideNotch = !current.hideNotch)) }) {
                        Text(
                            (if (current.hideNotch) "• 已开启 " else "") + "隐藏刘海",
                            style = MaterialTheme.typography.labelMedium,
                        )
                    }
                }
                Text(
                    if (current.hideNotch) "正文会铺到挖孔区域，边缘可能被遮挡。" else "自动避开状态栏与挖孔。",
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
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

/**
 * 第七阶段播放面板：播放/暂停、上一章/下一章、语速、定时关闭。
 * 只读取 [TtsController.State] 中的元数据与计数，不触碰正文或音频字节。
 */
@Composable
private fun PlaybackPanel(
    state: AppState,
    tts: TtsController.State,
    palette: ReaderPalette,
) {
    var showSleep by remember { mutableStateOf(false) }
    var showSpeed by remember { mutableStateOf(false) }

    Column(
        modifier = Modifier
            .fillMaxWidth()
            .padding(horizontal = 12.dp, vertical = 4.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            val progress = if (tts.total > 0) tts.segment.toFloat() / tts.total else 0f
            LinearProgressIndicator(
                progress = { progress.coerceIn(0f, 1f) },
                modifier = Modifier.weight(1f).height(3.dp),
                color = palette.dim,
                trackColor = palette.dim.copy(alpha = 0.2f),
            )
            Spacer(Modifier.width(8.dp))
            Text(
                "${tts.segment}/${tts.total}",
                color = palette.dim,
                style = MaterialTheme.typography.labelSmall,
            )
        }
        Spacer(Modifier.height(2.dp))
        Row(
            modifier = Modifier.fillMaxWidth(),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.SpaceEvenly,
        ) {
            TextButton(onClick = { state.tts.prevChapter() }) { Text("« 上一章", color = palette.dim) }
            TextButton(onClick = { if (tts.paused) state.tts.resume() else state.tts.pause() }) {
                Text(if (tts.paused) "▶ 继续" else "❚❚ 暂停", color = palette.dim)
            }
            TextButton(onClick = { state.tts.nextChapter() }) { Text("下一章 »", color = palette.dim) }
            TextButton(onClick = { showSpeed = true }) {
                Text("${fmt1(tts.speed)}x", color = palette.dim)
            }
            TextButton(onClick = { showSleep = true }) {
                Text(sleepLabel(tts), color = palette.dim)
            }
            TextButton(onClick = { state.tts.stop() }) { Text("停止", color = palette.dim) }
        }
    }

    if (showSpeed) {
        AlertDialog(
            onDismissRequest = { showSpeed = false },
            title = { Text("朗读语速") },
            text = {
                Column {
                    Text("${fmt1(state.tts.state.value.speed)}x", style = MaterialTheme.typography.labelMedium)
                    Slider(
                        value = state.tts.state.value.speed,
                        onValueChange = { state.tts.setSpeed(it) },
                        valueRange = 0.5f..2.0f,
                        steps = 5,
                    )
                    Text(
                        "客户端变速（AudioTrack time-stretch），不重新合成、不联网。",
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
            },
            confirmButton = { TextButton(onClick = { showSpeed = false }) { Text("完成") } },
        )
    }

    if (showSleep) {
        AlertDialog(
            onDismissRequest = { showSleep = false },
            title = { Text("定时关闭") },
            text = {
                Column(verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    listOf(
                        SleepMode.OFF to "不关闭",
                        SleepMode.MIN_15 to "15 分钟",
                        SleepMode.MIN_30 to "30 分钟",
                        SleepMode.MIN_60 to "60 分钟",
                        SleepMode.END_OF_CHAPTER to "播完当前章",
                    ).forEach { (mode, label) ->
                        TextButton(onClick = {
                            state.tts.setSleepMode(mode)
                            showSleep = false
                        }) {
                            Text(
                                (if (tts.sleepMode == mode) "• " else "") + label,
                                color = palette.dim,
                            )
                        }
                    }
                }
            },
            confirmButton = { TextButton(onClick = { showSleep = false }) { Text("取消") } },
        )
    }
}

/** 定时关闭按钮文案：显示已选模式与剩余时间（秒），仅元数据。 */
private fun sleepLabel(tts: TtsController.State): String = when (tts.sleepMode) {
    SleepMode.OFF -> "定时"
    SleepMode.END_OF_CHAPTER -> "播完停止"
    SleepMode.MIN_15, SleepMode.MIN_30, SleepMode.MIN_60 -> {
        val sec = (tts.sleepRemainingMs / 1000).coerceAtLeast(0)
        "定时 ${sec / 60}:${(sec % 60).toString().padStart(2, '0')}"
    }
}
