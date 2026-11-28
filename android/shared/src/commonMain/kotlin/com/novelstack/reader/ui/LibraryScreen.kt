package com.novelstack.reader.ui

import androidx.compose.foundation.background
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
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.Screen
import com.novelstack.reader.db.Books
import com.novelstack.reader.ui.components.AppleCheckbox
import com.novelstack.reader.ui.components.ApplePressable
import com.novelstack.reader.ui.components.BottomActionBar
import com.novelstack.reader.ui.components.CapsuleSearchField
import com.novelstack.reader.ui.components.DestructiveTextButton
import com.novelstack.reader.ui.components.RowContent
import com.novelstack.reader.ui.components.SelectHeader
import com.novelstack.reader.ui.components.SquircleCard

/**
 * 书架：iOS 分组内嵌列表 + 长按多选。
 *
 * 多选态下顶部标题换成「已选择 X 项」，每行左侧滑出正圆复选框，
 * 底部弹入操作栏。点行切换选中，而不是打开书。
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun LibraryScreen(state: AppState) {
    val books by state.books.collectAsState()
    val favOnly by state.favoritesOnly.collectAsState()
    val busy by state.busy.collectAsState()
    val notice by state.notice.collectAsState()
    val error by state.error.collectAsState()
    val offline by state.offline.collectAsState()

    var query by remember { mutableStateOf("") }
    var selecting by remember { mutableStateOf(false) }
    val selected = remember { mutableStateListOf<String>() }

    fun exitSelection() {
        selecting = false
        selected.clear()
    }

    // 过滤结果用 derivedStateOf 缓存：滚动/多选重组时不会重复扫描整个书架。
    val filtered by remember {
        androidx.compose.runtime.derivedStateOf {
            val available = state.visibleBooks(books)
            val q = query.trim()
            available
                .filter { !favOnly || it.is_favorite != 0L }
                .filter {
                    q.isEmpty() ||
                        it.title.contains(q, ignoreCase = true) ||
                        it.author.contains(q, ignoreCase = true)
                }
        }
    }
    val available = state.visibleBooks(books)
    val queryText = query.trim()
    val shown = filtered

    Scaffold(
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            Column {
                if (selecting) {
                    SelectHeader(count = selected.size, onCancel = { exitSelection() })
                } else {
                    TopAppBar(
                        title = { Text("书架", fontWeight = FontWeight.SemiBold) },
                        actions = {
                            androidx.compose.material3.TextButton(onClick = { state.sync() }, enabled = !busy) {
                                Text("同步")
                            }
                            androidx.compose.material3.TextButton(onClick = { state.screen.value = Screen.Search }) {
                                Text("搜索")
                            }
                        },
                        colors = TopAppBarDefaults.topAppBarColors(
                            containerColor = MaterialTheme.colorScheme.background,
                        ),
                    )
                }
            }
        },
        bottomBar = {
            BottomActionBar(visible = selecting) {
                Row(
                    modifier = Modifier.fillMaxWidth(),
                    horizontalArrangement = Arrangement.SpaceBetween,
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    DestructiveTextButton(
                        text = "移除本地缓存（${selected.size}）",
                        enabled = selected.isNotEmpty(),
                        onClick = {
                            state.removeLocalBooks(selected.toList())
                            exitSelection()
                        },
                    )
                    Text(
                        "仅删本地副本",
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
        },
    ) { pad ->
        Column(modifier = Modifier.fillMaxSize().padding(pad)) {
            if (busy) {
                LinearProgressIndicator(modifier = Modifier.fillMaxWidth())
            }
            notice?.let {
                Text(
                    it,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.primary,
                    modifier = Modifier.padding(horizontal = 20.dp, vertical = 6.dp),
                )
            }
            error?.let {
                Text(
                    it,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.error,
                    modifier = Modifier.padding(horizontal = 20.dp, vertical = 6.dp),
                )
            }

            if (!selecting) {
                CapsuleSearchField(
                    value = query,
                    onValueChange = { query = it },
                    modifier = Modifier.padding(horizontal = 16.dp, vertical = 6.dp),
                )
                FilterPills(
                    allCount = available.size,
                    favCount = available.count { it.is_favorite != 0L },
                    favOnly = favOnly,
                    onSelect = { state.favoritesOnly.value = it },
                )
            }

            if (shown.isEmpty()) {
                Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    Column(horizontalAlignment = Alignment.CenterHorizontally) {
                        if (busy) {
                            CircularProgressIndicator()
                        } else {
                            Text(
                                when {
                                    queryText.isNotEmpty() -> "没有匹配「$queryText」的书"
                                    favOnly -> "还没有收藏的书"
                                    offline -> "离线模式下没有可读的书"
                                    else -> "书库为空"
                                },
                                style = MaterialTheme.typography.titleMedium,
                            )
                            Spacer(Modifier.height(6.dp))
                            Text(
                                when {
                                    queryText.isNotEmpty() -> "换个关键词，或清空搜索"
                                    favOnly -> "在阅读页点右上角 ♥ 即可收藏"
                                    offline -> "恢复网络后同步，或先在在线状态下缓存章节"
                                    else -> "先在控制台上传书籍，然后点右上角「同步」"
                                },
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                    }
                }
            } else {
                LazyColumn(
                    modifier = Modifier.fillMaxSize(),
                    contentPadding = PaddingValues(start = 16.dp, end = 16.dp, top = 4.dp, bottom = 24.dp),
                    verticalArrangement = Arrangement.spacedBy(10.dp),
                ) {
                    items(shown, key = { it.id }) { book ->
                        BookRow(
                            book = book,
                            selecting = selecting,
                            checked = selected.contains(book.id),
                            onClick = {
                                if (selecting) {
                                    if (selected.contains(book.id)) selected.remove(book.id)
                                    else selected.add(book.id)
                                    if (selected.isEmpty()) selecting = false
                                } else {
                                    state.openBook(book.id)
                                }
                            },
                            onLongClick = {
                                if (!selecting) {
                                    selecting = true
                                    selected.add(book.id)
                                }
                            },
                        )
                    }
                }
            }
        }
    }
}

/** 全部 / 收藏：胶囊分段控件。 */
@Composable
private fun FilterPills(
    allCount: Int,
    favCount: Int,
    favOnly: Boolean,
    onSelect: (Boolean) -> Unit,
) {
    Row(
        modifier = Modifier.padding(horizontal = 16.dp, vertical = 2.dp),
        horizontalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        Pill(text = "全部 $allCount", selected = !favOnly) { onSelect(false) }
        Pill(text = "收藏 $favCount", selected = favOnly) { onSelect(true) }
    }
}

@Composable
private fun Pill(text: String, selected: Boolean, onClick: () -> Unit) {
    val bg = if (selected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.surface
    val fg = if (selected) MaterialTheme.colorScheme.onPrimary else MaterialTheme.colorScheme.onSurface
    Box(
        modifier = Modifier
            .clip(Squircle.capsule())
            .background(bg)
            .clickable(
                interactionSource = remember { MutableInteractionSource() },
                indication = null,
                onClick = onClick,
            )
            .padding(horizontal = 14.dp, vertical = 6.dp),
    ) {
        Text(text, style = MaterialTheme.typography.labelMedium, color = fg)
    }
}

/** 单本书行：G3 卡片；长按进入多选，选中时浅蓝底。 */
@Composable
private fun BookRow(
    book: Books,
    selecting: Boolean,
    checked: Boolean,
    onClick: () -> Unit,
    onLongClick: () -> Unit,
) {
    val favorite = book.is_favorite != 0L
    SquircleCard(modifier = Modifier.fillMaxWidth()) {
        ApplePressable(
            onClick = onClick,
            selected = checked,
            modifier = Modifier.fillMaxWidth(),
            onLongClick = if (selecting) null else onLongClick,
        ) {
            Row(
                modifier = Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 10.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                if (selecting) {
                    AppleCheckbox(checked = checked)
                    Spacer(Modifier.width(12.dp))
                }
                Box(
                    modifier = Modifier
                        .size(width = 42.dp, height = 56.dp)
                        .clip(Squircle.control())
                        .background(MaterialTheme.colorScheme.primary.copy(alpha = 0.12f)),
                    contentAlignment = Alignment.Center,
                ) {
                    Text(
                        text = book.title.take(1),
                        style = MaterialTheme.typography.titleLarge,
                        color = MaterialTheme.colorScheme.primary,
                        fontWeight = FontWeight.SemiBold,
                    )
                }
                Spacer(Modifier.width(12.dp))
                Column(modifier = Modifier.weight(1f)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(
                            book.title,
                            style = MaterialTheme.typography.bodyLarge,
                            fontWeight = FontWeight.Medium,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.weight(1f, fill = false),
                        )
                        if (favorite) {
                            Spacer(Modifier.width(6.dp))
                            Text("♥", color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
                        }
                    }
                    Spacer(Modifier.height(3.dp))
                    Text(
                        "${book.author.ifBlank { "未知作者" }} · ${book.chapter_count} 章 · ${formatBytes(book.size_bytes)}",
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
                if (!selecting) {
                    Text(
                        "›",
                        style = MaterialTheme.typography.titleLarge,
                        color = AppleTokens.TertiaryLabel,
                    )
                }
            }
        }
    }
}

fun formatBytes(n: Long): String {
    if (n < 1024) return "$n B"
    val kb = n / 1024.0
    if (kb < 1024) return format1(kb) + " KB"
    val mb = kb / 1024.0
    if (mb < 1024) return format1(mb) + " MB"
    return format1(mb / 1024.0) + " GB"
}

private fun format1(v: Double): String {
    val rounded = (v * 10).toLong() / 10.0
    return if (rounded % 1.0 == 0.0) rounded.toLong().toString() else rounded.toString()
}
