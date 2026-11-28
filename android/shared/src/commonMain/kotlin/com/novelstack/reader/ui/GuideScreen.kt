package com.novelstack.reader.ui

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.spring
import androidx.compose.animation.expandVertically
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.shrinkVertically
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
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
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.novelstack.reader.app.AppState
import com.novelstack.reader.ui.components.ApplePressable
import com.novelstack.reader.ui.components.GroupedList
import com.novelstack.reader.ui.components.SectionHeader

/**
 * 使用指南：图文 FAQ 折叠面板。
 *
 * 同一时刻只展开一条——手机上同时展开多条会把下面的内容推到很远处，
 * 用户反而找不到自己刚才点的是哪一条。
 */
private data class Faq(val q: String, val a: List<String>)

private val faqs = listOf(
    Faq(
        q = "怎么导入一本 TXT？",
        a = listOf(
            "控制台 → 书籍 → 上传，选中 txt 文件即可。编码（UTF-8 / GBK）会自动嗅探，识别不出来时可以在上传面板里手动指定。",
            "切章默认匹配「第 X 章」「Chapter N」等常见写法；如果你的文件用「卷一 · ×××」这类自定义标题，可以在同一面板填一条正则，预览确认后再入库。",
        ),
    ),
    Faq(
        q = "怎么切换音色、调语速？",
        a = listOf(
            "朗读过程中，阅读页底部会浮出播放条，点音色名即可在已配置的音色间切换；通知栏也能切上一章 / 下一章。",
            "语速在「设置 → 朗读」里调，范围 0.5–2.0 倍，下次开始朗读时生效。Web 端用的是系统语音，音色取决于浏览器与操作系统。",
        ),
    ),
    Faq(
        q = "局域网连接怎么配？",
        a = listOf(
            "手机与服务器在同一个 WiFi 下时：设置 → 服务器设置 → 局域网发现，列表里点一下就能连；或者用控制台「设备」页生成的 6 位配对码登录。",
            "换地址不会让你重新配对：设备令牌跟着设备走，地址只是入口。出门在外把地址切回公网域名即可，阅读进度不受影响。",
        ),
    ),
    Faq(
        q = "多端进度是怎么合并的？",
        a = listOf(
            "服务端按「章号 → 章内字符偏移」比较，保留更靠后的那一个，而不是「谁最后写入算谁」。所以 A 读到第 30 章、B 随手翻回第 12 章看一眼，A 不会被拽回第 12 章。",
            "代价是「主动往前跳章」不会同步出去。真需要回退时，在目标设备上手动跳一次章，等下一次同步即可。前台每 5 分钟合并一次。",
        ),
    ),
)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun GuideScreen(state: AppState, onBack: () -> Unit) {
    var open by remember { mutableIntStateOf(0) }

    Scaffold(
        containerColor = MaterialTheme.colorScheme.background,
        topBar = {
            TopAppBar(
                title = { Text("使用指南", fontWeight = FontWeight.SemiBold) },
                navigationIcon = {
                    TextButton(onClick = onBack) { Text("返回") }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.background,
                ),
            )
        },
    ) { pad ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(pad)
                .verticalScroll(rememberScrollState())
                .padding(horizontal = 16.dp),
        ) {
            SectionHeader("常见问题")
            GroupedList {
                faqs.forEachIndexed { i, f ->
                    FaqRow(
                        index = i,
                        faq = f,
                        expanded = open == i,
                        onToggle = { open = if (open == i) -1 else i },
                    )
                    if (i != faqs.lastIndex) {
                        HorizontalDivider(
                            modifier = Modifier.padding(start = 16.dp),
                            thickness = AppleTokens.HAIRLINE_DP.dp,
                            color = MaterialTheme.colorScheme.outlineVariant,
                        )
                    }
                }
            }

            SectionHeader("引导")
            GroupedList {
                ApplePressable(onClick = { state.resetGuides() }) {
                    Row(
                        modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 14.dp),
                        verticalAlignment = Alignment.CenterVertically,
                    ) {
                        Column(modifier = Modifier.weight(1f)) {
                            Text("重看新手引导", style = MaterialTheme.typography.bodyLarge)
                            Text(
                                "下一次进入书架会重新展示欢迎页与蒙层提示",
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                        Text(
                            "›",
                            style = MaterialTheme.typography.titleLarge,
                            color = AppleTokens.TertiaryLabel,
                        )
                    }
                }
            }

            Spacer(Modifier.height(18.dp))
            Text(
                "还有别的问题？控制台的「审计」页能看到每一次同步与失败的原始记录，排障时比截图有用。",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.padding(horizontal = 4.dp),
            )
            Spacer(Modifier.height(28.dp))
        }
    }
}

@Composable
private fun FaqRow(index: Int, faq: Faq, expanded: Boolean, onToggle: () -> Unit) {
    val rotation by animateFloatAsState(
        targetValue = if (expanded) 135f else 0f,
        animationSpec = spring(dampingRatio = 0.75f, stiffness = 400f),
        label = "faq-sign",
    )
    Column {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .clickable(
                    interactionSource = remember { MutableInteractionSource() },
                    indication = null,
                    onClick = onToggle,
                )
                .padding(start = 16.dp, end = 16.dp, top = 15.dp, bottom = 15.dp),
            verticalAlignment = Alignment.Top,
        ) {
            Text(
                text = (index + 1).toString().padStart(2, '0'),
                style = MaterialTheme.typography.labelSmall,
                color = AppleTokens.TertiaryLabel,
                letterSpacing = 1.sp,
                modifier = Modifier.padding(top = 2.dp, end = 10.dp),
            )
            Text(
                faq.q,
                style = MaterialTheme.typography.bodyLarge,
                modifier = Modifier.weight(1f),
            )
            // 加号：两条 1.5dp 线，展开时整体转 135° 变成 ×
            val signColor = MaterialTheme.colorScheme.onSurfaceVariant
            Box(modifier = Modifier.size(16.dp).padding(top = 2.dp).rotate(rotation)) {
                Canvas(modifier = Modifier.size(16.dp)) {
                    val c = signColor
                    val s = 1.5.dp.toPx()
                    drawLine(c, Offset(size.width * 0.1f, size.height / 2f), Offset(size.width * 0.9f, size.height / 2f), strokeWidth = s, cap = StrokeCap.Round)
                    drawLine(c, Offset(size.width / 2f, size.height * 0.1f), Offset(size.width / 2f, size.height * 0.9f), strokeWidth = s, cap = StrokeCap.Round)
                }
            }
        }
        AnimatedVisibility(
            visible = expanded,
            enter = expandVertically(animationSpec = spring(dampingRatio = 0.9f, stiffness = 380f)) + fadeIn(),
            exit = shrinkVertically(animationSpec = spring(dampingRatio = 0.9f, stiffness = 380f)) + fadeOut(),
        ) {
            Column(
                modifier = Modifier.padding(start = 46.dp, end = 16.dp, bottom = 15.dp),
                verticalArrangement = Arrangement.spacedBy(10.dp),
            ) {
                faq.a.forEach {
                    Text(
                        it,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                }
            }
        }
    }
}
