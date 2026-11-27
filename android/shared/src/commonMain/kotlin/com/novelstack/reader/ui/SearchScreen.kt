package com.novelstack.reader.ui

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.Screen

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SearchScreen(state: AppState) {
    val results by state.searchResults.collectAsState()
    var query by remember { mutableStateOf("") }
    // 终端防御：从阅读页划词「本地搜索」进入时，预填查询词并触发检索。
    val prefill by state.searchPrefill.collectAsState()
    LaunchedEffect(prefill) {
        if (prefill != null) {
            query = prefill!!
            state.search(prefill!!)
            state.searchPrefill.value = null
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("本地全文搜索") },
                navigationIcon = {
                    TextButton(onClick = { state.screen.value = Screen.Library }) { Text("‹ 书库") }
                },
            )
        },
    ) { pad ->
        Column(modifier = Modifier.fillMaxSize().padding(pad)) {
            OutlinedTextField(
                value = query,
                onValueChange = {
                    query = it
                    if (it.length >= 2) state.search(it)
                },
                label = { Text("关键词") },
                placeholder = { Text("搜索已缓存章节（SQLCipher FTS5）") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
                modifier = Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 8.dp),
            )
            Text(
                "仅在设备本地检索；未缓存的章节不会出现在结果里。",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 16.dp),
            )
            Spacer(Modifier.height(6.dp))
            if (results.isEmpty()) {
                Text(
                    if (query.isBlank()) "输入关键词开始搜索" else "没有命中",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier.padding(16.dp),
                )
            } else {
                LazyColumn(modifier = Modifier.fillMaxSize()) {
                    items(results, key = { "${it.bookId}:${it.chapterIndex}:${it.matchType}" }) { hit ->
                        Column(
                            modifier = Modifier
                                .fillMaxWidth()
                                .padding(horizontal = 16.dp, vertical = 10.dp),
                        ) {
                            Text(
                                "${hit.bookTitle} · ${hit.chapterTitle}",
                                style = MaterialTheme.typography.labelMedium,
                                color = MaterialTheme.colorScheme.primary,
                                maxLines = 1,
                                overflow = TextOverflow.Ellipsis,
                            )
                            Spacer(Modifier.height(4.dp))
                            Text(
                                hit.snippet,
                                style = MaterialTheme.typography.bodySmall,
                                fontWeight = FontWeight.Normal,
                                maxLines = 3,
                                overflow = TextOverflow.Ellipsis,
                            )
                            Spacer(Modifier.height(6.dp))
                            TextButton(
                                onClick = {
                                    if (hit.chapterIndex < 0) state.openBook(hit.bookId)
                                    else state.openAt(hit.bookId, hit.chapterIndex)
                                },
                                contentPadding = androidx.compose.foundation.layout.PaddingValues(0.dp),
                            ) {
                                Text(
                                    if (hit.chapterIndex < 0) "打开这本书"
                                    else "跳转到第 ${hit.chapterIndex + 1} 章",
                                )
                            }
                        }
                        HorizontalDivider()
                    }
                }
            }
        }
    }
}
