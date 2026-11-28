package com.novelstack.reader.ui

import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp

/**
 * G3 连续曲率圆角（Squircle）。
 *
 * 普通 `RoundedCornerShape` 是 G2 曲率（圆角与直边交界处二阶导数突变），
 * 视觉上会出现一圈「脏边」。iOS 的图标/卡片用的是超椭圆（superellipse），
 * 曲率连续变化，从直边到转角没有可见接缝。
 *
 * Android 侧由 `androidx.graphics:graphics-shapes` 的 RoundedPolygon 生成：
 * CornerRounding 的 smoothing 参数就是「曲率平滑系数」，0 退化为普通圆角，
 * 1 为最大连续曲率。这里取 0.6，接近 iOS 的观感且不会让转角显得过方。
 */
expect fun squircleShape(radius: Dp, smoothing: Float = 0.6f): Shape

/** 常用半径，避免调用点散落魔法数字。 */
object Squircle {
    /** 列表卡片 / 分组容器。 */
    val CardRadius = 16.dp

    /** 小控件：搜索框、按钮、标签。 */
    val ControlRadius = 12.dp

    /** 胶囊：半径给一个足够大的值，平台实现会按短边的一半截断。 */
    val CapsuleRadius = 999.dp

    /** 卡片形状（G3）。 */
    fun card(): Shape = squircleShape(CardRadius)

    /** 控件形状（G3）。 */
    fun control(): Shape = squircleShape(ControlRadius)

    /** 胶囊形状（G3）。 */
    fun capsule(): Shape = squircleShape(CapsuleRadius, smoothing = 0.7f)
}

@Composable
fun rememberSquircleCard(): Shape = androidx.compose.runtime.remember { Squircle.card() }
