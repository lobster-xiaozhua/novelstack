package com.novelstack.reader.ui.components

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.widthIn
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp

/** 空状态插画：三张极简线稿，全站复用。 */
enum class EmptyArt { Shelf, Clock, Doc }

/**
 * 空状态：线稿 + 一句话 + 一个出口。
 *
 * 空状态最容易犯的错是「只告诉用户这里没有东西」。所以 CTA 不是可选项：
 * 书架空了就把他带到发现页，而不是让用户自己猜下一步该点哪。
 */
@Composable
fun EmptyState(
    art: EmptyArt,
    title: String,
    desc: String,
    modifier: Modifier = Modifier,
    cta: String? = null,
    onCta: () -> Unit = {},
) {
    val ink = MaterialTheme.colorScheme.outline
    Column(
        modifier = modifier.padding(horizontal = 24.dp, vertical = 16.dp),
        horizontalAlignment = Alignment.CenterHorizontally,
        verticalArrangement = Arrangement.Center,
    ) {
        Canvas(modifier = Modifier.size(156.dp, 124.dp)) {
            val w = size.width
            val h = size.height
            val stroke = 1.6.dp.toPx()
            val thin = Stroke(width = stroke, cap = StrokeCap.Round)
            fun line(x1: Float, y1: Float, x2: Float, y2: Float, c: Color = ink, alpha: Float = 1f) =
                drawLine(c.copy(alpha = alpha), Offset(w * x1, h * y1), Offset(w * x2, h * y2), strokeWidth = stroke, cap = StrokeCap.Round)

            when (art) {
                EmptyArt.Shelf -> {
                    val frame = Path().apply {
                        moveTo(w * 0.12f, h * 0.20f)
                        lineTo(w * 0.12f, h * 0.86f)
                        lineTo(w * 0.88f, h * 0.86f)
                        lineTo(w * 0.88f, h * 0.20f)
                    }
                    drawPath(frame, ink, style = thin)
                    line(0.12f, 0.54f, 0.88f, 0.54f)
                    listOf(
                        floatArrayOf(0.20f, 0.32f, 0.07f, 0.22f),
                        floatArrayOf(0.30f, 0.27f, 0.07f, 0.27f),
                        floatArrayOf(0.40f, 0.37f, 0.07f, 0.17f),
                    ).forEach { r ->
                        drawRect(
                            color = ink,
                            topLeft = Offset(w * r[0], h * r[1]),
                            size = Size(w * r[2], h * r[3]),
                            style = thin,
                        )
                    }
                    // 一本斜靠的书
                    val lean = Path().apply {
                        moveTo(w * 0.51f, h * 0.54f)
                        lineTo(w * 0.57f, h * 0.30f)
                        lineTo(w * 0.64f, h * 0.33f)
                        lineTo(w * 0.58f, h * 0.54f)
                        close()
                    }
                    drawPath(lean, ink, style = thin)
                    drawRect(
                        color = ink,
                        topLeft = Offset(w * 0.20f, h * 0.68f),
                        size = Size(w * 0.07f, h * 0.18f),
                        style = thin,
                    )
                    drawRect(
                        color = ink,
                        topLeft = Offset(w * 0.30f, h * 0.74f),
                        size = Size(w * 0.07f, h * 0.12f),
                        style = thin,
                    )
                    line(0.70f, 0.68f, 0.80f, 0.68f, alpha = 0.4f)
                }

                EmptyArt.Clock -> {
                    drawCircle(ink, radius = w * 0.21f, center = Offset(w * 0.5f, h * 0.5f), style = thin)
                    line(0.5f, 0.5f, 0.5f, 0.34f)
                    line(0.5f, 0.5f, 0.62f, 0.57f)
                    line(0.5f, 0.14f, 0.5f, 0.20f, alpha = 0.45f)
                    line(0.5f, 0.80f, 0.5f, 0.86f, alpha = 0.45f)
                    line(0.14f, 0.50f, 0.20f, 0.50f, alpha = 0.45f)
                    line(0.80f, 0.50f, 0.86f, 0.50f, alpha = 0.45f)
                }

                EmptyArt.Doc -> {
                    drawRoundRect(
                        color = ink,
                        topLeft = Offset(w * 0.20f, h * 0.16f),
                        size = Size(w * 0.60f, h * 0.66f),
                        cornerRadius = androidx.compose.ui.geometry.CornerRadius(8.dp.toPx()),
                        style = thin,
                    )
                    line(0.30f, 0.34f, 0.70f, 0.34f, alpha = 0.5f)
                    line(0.30f, 0.48f, 0.62f, 0.48f, alpha = 0.4f)
                    line(0.30f, 0.62f, 0.66f, 0.62f, alpha = 0.3f)
                }
            }
        }
        Spacer(Modifier.height(18.dp))
        Text(
            title,
            style = MaterialTheme.typography.titleMedium,
            color = MaterialTheme.colorScheme.onSurface,
            textAlign = TextAlign.Center,
        )
        Spacer(Modifier.height(6.dp))
        Text(
            desc,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            textAlign = TextAlign.Center,
        )
        if (cta != null) {
            Spacer(Modifier.height(18.dp))
            ApplePrimaryButton(
                text = cta,
                onClick = onCta,
                modifier = Modifier.widthIn(max = 220.dp),
            )
        }
    }
}
