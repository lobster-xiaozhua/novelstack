package com.novelstack.reader.ui

import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Outline
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.graphics.asComposePath
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.LayoutDirection
import androidx.graphics.shapes.CornerRounding
import androidx.graphics.shapes.RoundedPolygon
import androidx.graphics.shapes.rectangle
import androidx.graphics.shapes.toPath

/**
 * Android 侧 G3 连续曲率实现。
 *
 * 每个尺寸实时构造 RoundedPolygon（4 个顶点 + 平滑转角），再转成 Path 作为
 * Outline。Compose 会对 Shape 的 createOutline 结果按尺寸缓存，实际开销很小。
 *
 * 半径按短边一半截断：胶囊形状传入 999.dp 时自动变成「真正的胶囊」，
 * 而不是画出越界的曲线。
 */
actual fun squircleShape(radius: Dp, smoothing: Float): Shape = SquircleShapeImpl(radius, smoothing)

private class SquircleShapeImpl(
    private val radius: Dp,
    private val smoothing: Float,
) : Shape {
    override fun createOutline(
        size: Size,
        layoutDirection: LayoutDirection,
        density: Density,
    ): Outline {
        if (size.width <= 0f || size.height <= 0f) {
            return Outline.Generic(Path())
        }
        val r = with(density) { radius.toPx() }
            .coerceAtMost(minOf(size.width, size.height) / 2f)
            .coerceAtLeast(0f)
        val rounded = RoundedPolygon.rectangle(
            width = size.width,
            height = size.height,
            rounding = CornerRounding(radius = r, smoothing = smoothing.coerceIn(0f, 1f)),
            centerX = size.width / 2f,
            centerY = size.height / 2f,
        )
        return Outline.Generic(rounded.toPath().asComposePath())
    }

    override fun equals(other: Any?): Boolean {
        if (this === other) return true
        if (other !is SquircleShapeImpl) return false
        return radius == other.radius && smoothing == other.smoothing
    }

    override fun hashCode(): Int = 31 * radius.hashCode() + smoothing.hashCode()
}
