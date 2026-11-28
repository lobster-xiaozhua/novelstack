package com.novelstack.reader.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import kotlinx.coroutines.launch
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.novelstack.reader.ui.components.ApplePrimaryButton

/**
 * 欢迎页：HorizontalPager 三页，纯浅灰底 + 细线条插画。
 *
 * 刻意不放 Logo、不放插画阴影、不放渐变：整屏只有「黑线 + 一句话」，
 * 让三件事被记住——私密、沉浸、同步。点「跳过」与看完最后一页的效果相同，
 * 都会把状态写进 DataStore，下次不再出现。
 */
private data class OnboardPage(
    val no: String,
    val title: String,
    val desc: String,
    val art: Art,
)

private enum class Art { Shield, Book, Sync }

private val pages = listOf(
    OnboardPage(
        no = "01",
        title = "数据绝对私密",
        desc = "正文以 AES-GCM 信封加密存放，密钥只在你的设备上解开。服务端拿到的只有密文。",
        art = Art.Shield,
    ),
    OnboardPage(
        no = "02",
        title = "沉浸阅读",
        desc = "左右滑动翻页，长按取词，一键听书。排版按中文长文的节奏调过行高与字距。",
        art = Art.Book,
    ),
    OnboardPage(
        no = "03",
        title = "多端同步",
        desc = "手机读到第几章，网页打开就接着读。按最远位置合并，谁也不会把谁拽回去。",
        art = Art.Sync,
    ),
)

@Composable
fun OnboardingScreen(onDone: () -> Unit) {
    val pager = rememberPagerState { pages.size }
    val scope = androidx.compose.runtime.rememberCoroutineScope()
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(MaterialTheme.colorScheme.background),
    ) {
        HorizontalPager(state = pager, modifier = Modifier.fillMaxSize()) { index ->
            val page = pages[index]
            Column(
                modifier = Modifier.fillMaxSize().padding(horizontal = 32.dp, vertical = 24.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
                verticalArrangement = Arrangement.Center,
            ) {
                OnboardArt(page.art)
                Spacer(Modifier.height(28.dp))
                Text(
                    page.no,
                    style = MaterialTheme.typography.labelSmall,
                    letterSpacing = 4.sp,
                    fontWeight = FontWeight.SemiBold,
                    color = MaterialTheme.colorScheme.primary,
                )
                Spacer(Modifier.height(12.dp))
                Text(
                    page.title,
                    style = MaterialTheme.typography.headlineLarge,
                    fontWeight = FontWeight.Bold,
                    letterSpacing = (-0.6).sp,
                    textAlign = TextAlign.Center,
                )
                Spacer(Modifier.height(12.dp))
                Text(
                    page.desc,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    textAlign = TextAlign.Center,
                )
            }
        }

        // 右上角「跳过」：与「开始使用」同样的落盘结果。
        Text(
            text = "跳过",
            style = MaterialTheme.typography.bodyLarge,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier
                .align(Alignment.TopEnd)
                .statusBarsPadding()
                .padding(8.dp)
                .clip(remember { Squircle.control() })
                .clickable(onClick = onDone)
                .padding(horizontal = 14.dp, vertical = 10.dp),
        )

        Column(
            modifier = Modifier
                .align(Alignment.BottomCenter)
                .navigationBarsPadding()
                .padding(start = 24.dp, end = 24.dp, bottom = 22.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Row(
                horizontalArrangement = Arrangement.spacedBy(6.dp),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                repeat(pages.size) { i ->
                    val active = i == pager.currentPage
                    Box(
                        modifier = Modifier
                            .height(6.dp)
                            .width(if (active) 20.dp else 6.dp)
                            .clip(remember { Squircle.capsule() })
                            .background(
                                if (active) MaterialTheme.colorScheme.primary
                                else Color(0xFFC7C7CC),
                            ),
                    )
                }
            }
            Spacer(Modifier.height(18.dp))
            val last = pager.currentPage == pages.lastIndex
            ApplePrimaryButton(
                text = if (last) "开始使用" else "下一步",
                onClick = {
                    if (last) onDone()
                    else scope.launch { pager.animateScrollToPage(pager.currentPage + 1) }
                },
                modifier = Modifier.fillMaxWidth(),
            )
        }
    }
}

// ---------- 细线条插画：只用描边，坐标按比例给，换尺寸不变形 ----------

@Composable
private fun OnboardArt(kind: Art) {
    val ink = MaterialTheme.colorScheme.onSurface
    Canvas(modifier = Modifier.size(220.dp, 168.dp)) {
        val w = size.width
        val h = size.height
        val stroke = 1.6.dp.toPx()
        val thin = Stroke(width = stroke, cap = StrokeCap.Round)
        when (kind) {
            Art.Shield -> {
                val shield = Path().apply {
                    moveTo(w * 0.50f, h * 0.08f)
                    lineTo(w * 0.86f, h * 0.22f)
                    lineTo(w * 0.86f, h * 0.52f)
                    cubicTo(w * 0.86f, h * 0.72f, w * 0.70f, h * 0.87f, w * 0.50f, h * 0.94f)
                    cubicTo(w * 0.30f, h * 0.87f, w * 0.14f, h * 0.72f, w * 0.14f, h * 0.52f)
                    lineTo(w * 0.14f, h * 0.22f)
                    close()
                }
                drawPath(shield, ink, style = thin)
                drawRoundRect(
                    color = ink,
                    topLeft = Offset(w * 0.35f, h * 0.38f),
                    size = Size(w * 0.30f, h * 0.21f),
                    cornerRadius = androidx.compose.ui.geometry.CornerRadius(7.dp.toPx()),
                    style = thin,
                )
                drawLine(ink, Offset(w * 0.36f, h * 0.41f), Offset(w * 0.50f, h * 0.53f), strokeWidth = stroke, cap = StrokeCap.Round)
                drawLine(ink, Offset(w * 0.64f, h * 0.41f), Offset(w * 0.50f, h * 0.53f), strokeWidth = stroke, cap = StrokeCap.Round)
                drawLine(
                    ink.copy(alpha = 0.3f),
                    start = Offset(w * 0.50f, h * 0.94f),
                    end = Offset(w * 0.50f, h * 1.00f),
                    strokeWidth = stroke,
                    pathEffect = PathEffect.dashPathEffect(floatArrayOf(3.dp.toPx(), 5.dp.toPx())),
                )
            }

            Art.Book -> {
                val left = Path().apply {
                    moveTo(w * 0.10f, h * 0.28f)
                    quadraticBezierTo(w * 0.30f, h * 0.20f, w * 0.47f, h * 0.30f)
                    lineTo(w * 0.47f, h * 0.80f)
                    quadraticBezierTo(w * 0.30f, h * 0.70f, w * 0.10f, h * 0.78f)
                    close()
                }
                val right = Path().apply {
                    moveTo(w * 0.90f, h * 0.28f)
                    quadraticBezierTo(w * 0.70f, h * 0.20f, w * 0.53f, h * 0.30f)
                    lineTo(w * 0.53f, h * 0.80f)
                    quadraticBezierTo(w * 0.70f, h * 0.70f, w * 0.90f, h * 0.78f)
                    close()
                }
                drawPath(left, ink, style = thin)
                drawPath(right, ink, style = thin)
                repeat(2) { i ->
                    val y = h * (0.42f + i * 0.11f)
                    drawLine(ink.copy(alpha = 0.45f), Offset(w * 0.18f, y), Offset(w * 0.40f, y), strokeWidth = stroke, cap = StrokeCap.Round)
                    drawLine(ink.copy(alpha = 0.45f), Offset(w * 0.60f, y), Offset(w * 0.82f, y), strokeWidth = stroke, cap = StrokeCap.Round)
                }
                // 声波：两段同心弧，暗示「可以听」
                drawArc(
                    color = ink.copy(alpha = 0.45f),
                    startAngle = -50f, sweepAngle = 100f, useCenter = false,
                    topLeft = Offset(w * 0.74f, h * 0.02f),
                    size = Size(w * 0.26f, h * 0.26f),
                    style = Stroke(width = stroke, cap = StrokeCap.Round),
                )
                drawArc(
                    color = ink.copy(alpha = 0.22f),
                    startAngle = -50f, sweepAngle = 100f, useCenter = false,
                    topLeft = Offset(w * 0.66f, h * -0.05f),
                    size = Size(w * 0.42f, h * 0.42f),
                    style = Stroke(width = stroke, cap = StrokeCap.Round),
                )
            }

            Art.Sync -> {
                drawRoundRect(
                    color = ink,
                    topLeft = Offset(w * 0.12f, h * 0.24f),
                    size = Size(w * 0.28f, h * 0.56f),
                    cornerRadius = androidx.compose.ui.geometry.CornerRadius(9.dp.toPx()),
                    style = thin,
                )
                drawLine(ink.copy(alpha = 0.5f), Offset(w * 0.21f, h * 0.73f), Offset(w * 0.31f, h * 0.73f), strokeWidth = stroke, cap = StrokeCap.Round)
                drawRoundRect(
                    color = ink,
                    topLeft = Offset(w * 0.56f, h * 0.30f),
                    size = Size(w * 0.32f, h * 0.30f),
                    cornerRadius = androidx.compose.ui.geometry.CornerRadius(7.dp.toPx()),
                    style = thin,
                )
                drawLine(ink, Offset(w * 0.50f, h * 0.80f), Offset(w * 0.94f, h * 0.80f), strokeWidth = stroke, cap = StrokeCap.Round)
                drawLine(ink.copy(alpha = 0.5f), Offset(w * 0.72f, h * 0.60f), Offset(w * 0.72f, h * 0.80f), strokeWidth = stroke, cap = StrokeCap.Round)
                // 箭头
                drawLine(ink, Offset(w * 0.44f, h * 0.45f), Offset(w * 0.54f, h * 0.45f), strokeWidth = stroke, cap = StrokeCap.Round)
                drawLine(ink, Offset(w * 0.50f, h * 0.41f), Offset(w * 0.54f, h * 0.45f), strokeWidth = stroke, cap = StrokeCap.Round)
                drawLine(ink, Offset(w * 0.50f, h * 0.49f), Offset(w * 0.54f, h * 0.45f), strokeWidth = stroke, cap = StrokeCap.Round)
                drawArc(
                    color = ink.copy(alpha = 0.3f),
                    startAngle = 200f, sweepAngle = 140f, useCenter = false,
                    topLeft = Offset(w * 0.56f, h * 0.06f),
                    size = Size(w * 0.24f, h * 0.18f),
                    style = Stroke(width = stroke, cap = StrokeCap.Round),
                )
            }
        }
    }
}
