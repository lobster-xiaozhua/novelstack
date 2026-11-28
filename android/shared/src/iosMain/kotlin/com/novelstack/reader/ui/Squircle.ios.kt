package com.novelstack.reader.ui

import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.ui.graphics.Shape
import androidx.compose.ui.unit.Dp

/**
 * iOS 侧暂用等价圆角：UIKit 的 `continuous` corner curve 本身就是 G3，
 * SwiftUI 的 RoundedRectangle(cornerRadius:style: .continuous) 可直接映射。
 * 第五阶段不验收 iOS 运行时，这里先保证源码可编译（macOS 上才参与构建）。
 */
actual fun squircleShape(radius: Dp, smoothing: Float): Shape = RoundedCornerShape(radius)
