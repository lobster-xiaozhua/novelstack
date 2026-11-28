package com.novelstack.reader.ui.components

import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.spring
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.clickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.BoxWithConstraints
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.Outline
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.IntSize
import androidx.compose.ui.unit.dp
import com.novelstack.reader.ui.squircleShape
import kotlinx.coroutines.delay
import kotlin.math.min
import kotlin.math.roundToInt

/**
 * 蒙层高亮引导的一步。
 *
 * [target] 是被高亮组件在根坐标系里的位置（px），由宿主页面用 onGloballyPositioned 量出来。
 * 做成数据而不是选择器：Compose 没有 querySelector，靠 tag 反查既脆弱又慢。
 */
data class CoachStep(
    val target: Rect,
    val title: String,
    val body: String,
)

/**
 * 蒙层高亮（Spotlight）。
 *
 * 关键在 `graphicsLayer(CompositingStrategy.Offscreen)` + `BlendMode.Clear`：
 * 先整屏铺 60% 黑，再用 G3 圆角路径以 Clear 模式擦掉目标区域——擦完是**真透明**，
 * 底下被高亮的组件依然活着（能点、会动），而不是盖一块形状相同的白块。
 */
@Composable
fun CoachMarks(
    visible: Boolean,
    steps: List<CoachStep>,
    onFinish: () -> Unit,
) {
    if (!visible || steps.isEmpty()) return

    var index by remember { mutableIntStateOf(0) }
    var closing by remember { mutableStateOf(false) }
    var bubbleSize by remember { mutableStateOf(IntSize.Zero) }

    val step = steps[index.coerceIn(0, steps.lastIndex)]
    val alpha by animateFloatAsState(
        targetValue = if (closing) 0f else 1f,
        animationSpec = spring(dampingRatio = 0.75f, stiffness = 400f),
        label = "coach-alpha",
    )
    LaunchedEffect(closing) {
        if (closing) {
            delay(200)
            onFinish()
        }
    }

    val density = LocalDensity.current
    val layoutDirection = LocalLayoutDirection.current
    val padPx = with(density) { 8.dp.toPx() }
    val gapPx = with(density) { 12.dp.toPx() }
    val edgePx = with(density) { 12.dp.toPx() }
    val maxBubblePx = with(density) { 296.dp.toPx() }

    val advance: () -> Unit = { if (index >= steps.lastIndex) closing = true else index += 1 }
    val hole = Rect(
        left = (step.target.left - padPx).coerceAtLeast(0f),
        top = (step.target.top - padPx).coerceAtLeast(0f),
        right = step.target.right + padPx,
        bottom = step.target.bottom + padPx,
    )

    BoxWithConstraints(
        modifier = Modifier.fillMaxSize().graphicsLayer { this.alpha = alpha },
    ) {
        val screenW = constraints.maxWidth.toFloat()
        val screenH = constraints.maxHeight.toFloat()
        val bubbleW = min(maxBubblePx, screenW - edgePx * 2)
        val bx = (hole.center.x - bubbleW / 2f).coerceIn(edgePx, (screenW - bubbleW - edgePx).coerceAtLeast(edgePx))
        val below = hole.bottom + gapPx + bubbleSize.height + 16f < screenH
        val by = if (below) hole.bottom + gapPx else hole.top - gapPx - bubbleSize.height

        Canvas(
            modifier = Modifier
                .fillMaxSize()
                .graphicsLayer(compositingStrategy = CompositingStrategy.Offscreen)
                .clickable(
                    interactionSource = remember { MutableInteractionSource() },
                    indication = null,
                    onClick = advance,
                ),
        ) {
            drawRect(Color.Black.copy(alpha = 0.6f))

            val outline = squircleShape(14.dp).createOutline(
                size = Size(hole.width, hole.height),
                layoutDirection = layoutDirection,
                density = this,
            )
            val path = Path()
            when (outline) {
                is Outline.Generic -> path.addPath(outline.path)
                is Outline.Rounded -> path.addRoundRect(outline.roundRect)
                else -> path.addRect(hole)
            }
            path.translate(Offset(hole.left, hole.top))

            // 擦孔：这一笔之后孔内透明，露出底下的真实组件。
            drawPath(path, Color.Black, blendMode = BlendMode.Clear)
            // 孔边缘 2dp 白描边：深色蒙层里给高亮区一个清晰边界。
            drawPath(path, Color.White, style = Stroke(width = 2.dp.toPx()))
        }

        Column(
            modifier = Modifier
                .offset { IntOffset(bx.roundToInt(), by.roundToInt()) }
                .width(with(density) { bubbleW.toDp() })
                .onSizeChanged { if (it != bubbleSize) bubbleSize = it },
        ) {
            if (below) Arrow(centerPx = hole.center.x - bx, widthPx = bubbleW, up = true)
            Bubble(
                step = step,
                index = index,
                total = steps.size,
                onNext = advance,
                onSkip = { closing = true },
            )
            if (!below) Arrow(centerPx = hole.center.x - bx, widthPx = bubbleW, up = false)
        }
    }
}

/** 指向目标的小三角：等腰三角用 Path 画，避免旋转方块带来的半像素毛边。 */
@Composable
private fun Arrow(centerPx: Float, widthPx: Float, up: Boolean) {
    val density = LocalDensity.current
    val w = 14.dp
    val h = 7.dp
    val wPx = with(density) { w.toPx() }
    val hPx = with(density) { h.toPx() }
    val inset = with(density) { 14.dp.toPx() }
    val leftPx = (centerPx - wPx / 2f).coerceIn(inset, (widthPx - wPx - inset).coerceAtLeast(inset))
    Row(modifier = Modifier.width(with(density) { widthPx.toDp() })) {
        Spacer(Modifier.width(with(density) { leftPx.toDp() }))
        Canvas(modifier = Modifier.size(w, h)) {
            val p = Path().apply {
                if (up) {
                    moveTo(0f, hPx)
                    lineTo(wPx / 2f, 0f)
                    lineTo(wPx, hPx)
                } else {
                    moveTo(0f, 0f)
                    lineTo(wPx / 2f, hPx)
                    lineTo(wPx, 0f)
                }
                close()
            }
            drawPath(p, Color.White)
        }
    }
}

@Composable
private fun Bubble(
    step: CoachStep,
    index: Int,
    total: Int,
    onNext: () -> Unit,
    onSkip: () -> Unit,
) {
    SquircleCard(modifier = Modifier.fillMaxWidth()) {
        Column(modifier = Modifier.padding(start = 16.dp, end = 16.dp, top = 15.dp, bottom = 11.dp)) {
            Text(
                step.title,
                style = MaterialTheme.typography.titleSmall,
                fontWeight = FontWeight.SemiBold,
            )
            Spacer(Modifier.height(6.dp))
            Text(
                step.body,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(10.dp))
            Row(
                modifier = Modifier.fillMaxWidth(),
                horizontalArrangement = Arrangement.SpaceBetween,
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    "跳过",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    modifier = Modifier
                        .clickable(onClick = onSkip)
                        .padding(vertical = 4.dp, horizontal = 2.dp),
                )
                Text(
                    if (index >= total - 1) "知道了" else "下一步（${index + 1}/$total）",
                    style = MaterialTheme.typography.bodyMedium,
                    fontWeight = FontWeight.SemiBold,
                    color = MaterialTheme.colorScheme.primary,
                    modifier = Modifier
                        .clickable(onClick = onNext)
                        .padding(vertical = 4.dp, horizontal = 2.dp),
                )
            }
        }
    }
}
