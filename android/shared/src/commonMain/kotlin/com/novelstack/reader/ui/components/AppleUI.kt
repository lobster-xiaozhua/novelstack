package com.novelstack.reader.ui.components

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.Spring
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.spring
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.animation.slideInVertically
import androidx.compose.animation.slideOutVertically
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.interaction.MutableInteractionSource
import androidx.compose.foundation.interaction.collectIsPressedAsState
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.scale
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxHeight
import com.novelstack.reader.ui.AppleTokens
import com.novelstack.reader.ui.Squircle

/**
 * 苹果风基础组件。
 *
 * 设计约束（强执行）：
 *  - 所有容器走 G3 连续曲率（Squircle），不使用 RoundedCornerShape；
 *  - 卡片白底 + 0.5dp 内嵌边框，**没有** shadow、没有 blur；
 *  - 交互态用「浅蓝底 + 蓝色字」和 0.98 缩放，替代 Material 默认水波纹。
 */

/** 弹簧参数：dampingRatio 0.75 有轻微回弹但不过冲，符合 iOS 手感。 */
private fun <T> appleSpring() = spring<T>(
    dampingRatio = 0.75f,
    stiffness = Spring.StiffnessMediumLow,
)

/** 卡片：纯白（暗色下 #1C1C1E）+ 极细描边，G3 圆角。 */
@Composable
fun SquircleCard(
    modifier: Modifier = Modifier,
    contentPadding: androidx.compose.foundation.layout.PaddingValues =
        androidx.compose.foundation.layout.PaddingValues(0.dp),
    content: @Composable () -> Unit,
) {
    val shape = remember { Squircle.card() }
    val bg = MaterialTheme.colorScheme.surface
    val line = MaterialTheme.colorScheme.outlineVariant
    Box(
        modifier = modifier
            .clip(shape)
            .background(bg)
            .border(AppleTokens.HAIRLINE_DP.dp, line, shape),
    ) {
        Box(modifier = Modifier.padding(contentPadding)) { content() }
    }
}

/**
 * 可按压容器：按下时 scale(0.98) 并叠一层浅蓝。
 * 用于卡片/行/按钮，替代 ripple。
 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun ApplePressable(
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    selected: Boolean = false,
    shape: androidx.compose.ui.graphics.Shape = remember { Squircle.card() },
    onLongClick: (() -> Unit)? = null,
    content: @Composable () -> Unit,
) {
    val interaction = remember { MutableInteractionSource() }
    val pressed by interaction.collectIsPressedAsState()
    val scale by animateFloatAsState(
        targetValue = if (pressed) 0.98f else 1f,
        animationSpec = appleSpring(),
        label = "apple-press",
    )
    val overlay = when {
        pressed -> MaterialTheme.colorScheme.primary.copy(alpha = 0.10f)
        selected -> MaterialTheme.colorScheme.primary.copy(alpha = 0.08f)
        else -> Color.Transparent
    }
    val gestures = if (onLongClick != null) {
        Modifier.combinedClickable(
            interactionSource = interaction,
            indication = null,
            enabled = enabled,
            onLongClick = onLongClick,
            onClick = onClick,
        )
    } else {
        Modifier.clickable(
            interactionSource = interaction,
            indication = null,
            enabled = enabled,
            onClick = onClick,
        )
    }
    Box(
        modifier = modifier
            .scale(scale)
            .clip(shape)
            .background(overlay)
            .then(gestures),
    ) { content() }
}

/** 分组标题：iOS 设置页里的小号灰色全大写风格。 */
@Composable
fun SectionHeader(text: String, modifier: Modifier = Modifier) {
    Text(
        text = text,
        style = MaterialTheme.typography.labelMedium,
        color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = modifier.padding(start = 20.dp, top = 18.dp, bottom = 8.dp),
    )
}

/**
 * 分组内嵌列表：卡片内多行，行高 52dp，分割线左侧缩进（与文字对齐）。
 * 最后一行不画线。
 */
@Composable
fun GroupedList(
    modifier: Modifier = Modifier,
    content: @Composable () -> Unit,
) {
    SquircleCard(modifier = modifier.fillMaxWidth()) {
        Column { content() }
    }
}

/** 列表行内容（不含点击、不含分割线），供 GroupedList 组合。 */
@Composable
fun RowContent(
    title: String,
    subtitle: String? = null,
    leading: (@Composable () -> Unit)? = null,
    trailing: (@Composable () -> Unit)? = null,
    titleColor: Color = MaterialTheme.colorScheme.onSurface,
    showDivider: Boolean = false,
) {
    Column {
        Row(
            modifier = Modifier
                .fillMaxWidth()
                .heightIn(min = 52.dp)
                .padding(start = if (leading == null) 16.dp else 12.dp, end = 12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            if (leading != null) {
                leading()
                Spacer(Modifier.width(12.dp))
            }
            Column(modifier = Modifier.weight(1f).padding(vertical = 8.dp)) {
                Text(
                    title,
                    style = MaterialTheme.typography.bodyLarge,
                    color = titleColor,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                if (!subtitle.isNullOrBlank()) {
                    Spacer(Modifier.height(2.dp))
                    Text(
                        subtitle,
                        style = MaterialTheme.typography.bodySmall,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
            trailing?.invoke()
        }
        if (showDivider) {
            // 左侧缩进 16dp：与文字左缘齐平，是 iOS 分组列表的标志性细节。
            HorizontalDivider(
                modifier = Modifier.padding(start = 16.dp),
                thickness = AppleTokens.HAIRLINE_DP.dp,
                color = MaterialTheme.colorScheme.outlineVariant,
            )
        }
    }
}

/** 胶囊搜索框：浅灰底、无边框、左侧细线放大镜。 */
@Composable
fun CapsuleSearchField(
    value: String,
    onValueChange: (String) -> Unit,
    modifier: Modifier = Modifier,
    placeholder: String = "搜索书名、作者或章节",
) {
    val shape = remember { Squircle.capsule() }
    Row(
        modifier = modifier
            .fillMaxWidth()
            .height(40.dp)
            .clip(shape)
            .background(MaterialTheme.colorScheme.surfaceVariant)
            .padding(horizontal = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        MagnifierIcon(color = MaterialTheme.colorScheme.onSurfaceVariant)
        Spacer(Modifier.width(8.dp))
        Box(modifier = Modifier.weight(1f), contentAlignment = Alignment.CenterStart) {
            if (value.isEmpty()) {
                Text(
                    placeholder,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            BasicTextField(
                value = value,
                onValueChange = onValueChange,
                singleLine = true,
                textStyle = MaterialTheme.typography.bodyMedium.copy(
                    color = MaterialTheme.colorScheme.onSurface,
                ),
                cursorBrush = SolidColor(MaterialTheme.colorScheme.primary),
                modifier = Modifier.fillMaxWidth(),
            )
        }
        if (value.isNotEmpty()) {
            Text(
                "✕",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier
                    .clip(CircleShape)
                    .clickable { onValueChange("") }
                    .padding(6.dp),
            )
        }
    }
}

/** 细线放大镜：圆 + 45° 手柄，1.6dp 描边。 */
@Composable
private fun MagnifierIcon(color: Color) {
    Canvas(modifier = Modifier.size(15.dp)) {
        val stroke = Stroke(width = 1.6.dp.toPx())
        val r = size.minDimension * 0.32f
        val c = Offset(size.width * 0.44f, size.height * 0.44f)
        drawCircle(color = color, radius = r, center = c, style = stroke)
        drawLine(
            color = color,
            start = Offset(c.x + r * 0.72f, c.y + r * 0.72f),
            end = Offset(size.width * 0.92f, size.height * 0.92f),
            strokeWidth = 1.6.dp.toPx(),
        )
    }
}

/** 正圆复选框：未选空心细边，选中蓝底白勾；勾用两段线画，避免字体渲染差异。 */
@Composable
fun AppleCheckbox(checked: Boolean, modifier: Modifier = Modifier) {
    val bg = if (checked) MaterialTheme.colorScheme.primary else Color.Transparent
    val line = if (checked) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.outline
    Box(
        modifier = modifier
            .size(22.dp)
            .clip(CircleShape)
            .background(bg)
            .border(1.2.dp, line, CircleShape),
        contentAlignment = Alignment.Center,
    ) {
        if (checked) {
            Canvas(modifier = Modifier.size(12.dp)) {
                val w = size.width
                val h = size.height
                drawLine(
                    Color.White,
                    Offset(w * 0.12f, h * 0.52f),
                    Offset(w * 0.42f, h * 0.80f),
                    strokeWidth = 2.dp.toPx(),
                    cap = androidx.compose.ui.graphics.StrokeCap.Round,
                )
                drawLine(
                    Color.White,
                    Offset(w * 0.42f, h * 0.80f),
                    Offset(w * 0.92f, h * 0.22f),
                    strokeWidth = 2.dp.toPx(),
                    cap = androidx.compose.ui.graphics.StrokeCap.Round,
                )
            }
        }
    }
}

/** 顶部多选标题栏内容：取消 / 已选择 X 项。 */
@Composable
fun SelectHeader(
    count: Int,
    onCancel: () -> Unit,
    modifier: Modifier = Modifier,
) {
    Row(
        modifier = modifier
            .fillMaxWidth()
            .height(56.dp)
            .padding(horizontal = 16.dp),
        verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.SpaceBetween,
    ) {
        Text(
            "取消",
            style = MaterialTheme.typography.bodyLarge,
            color = MaterialTheme.colorScheme.primary,
            modifier = Modifier
                .clip(CircleShape)
                .clickable(onClick = onCancel)
                .padding(horizontal = 8.dp, vertical = 6.dp),
        )
        Text(
            "已选择 $count 项",
            style = MaterialTheme.typography.titleMedium,
            fontWeight = FontWeight.SemiBold,
        )
        // 占位，保证标题真正居中。
        Spacer(Modifier.width(48.dp))
    }
}

/**
 * 底部操作栏：从下方弹入（spring），白底 + 细上边线。
 * actions 里的危险操作传红色，由调用方决定。
 */
@Composable
fun BottomActionBar(
    visible: Boolean,
    modifier: Modifier = Modifier,
    content: @Composable () -> Unit,
) {
    AnimatedVisibility(
        visible = visible,
        enter = slideInVertically(animationSpec = appleSpring()) { it } + fadeIn(),
        exit = slideOutVertically(animationSpec = appleSpring()) { it } + fadeOut(),
        modifier = modifier,
    ) {
        Box(
            modifier = Modifier
                .fillMaxWidth()
                .background(MaterialTheme.colorScheme.surface)
                .border(
                    width = AppleTokens.HAIRLINE_DP.dp,
                    color = MaterialTheme.colorScheme.outlineVariant,
                )
                .padding(horizontal = 16.dp, vertical = 10.dp),
        ) { content() }
    }
}

/** 危险操作按钮：红字，无填充（iOS 的 destructive 样式）。 */
@Composable
fun DestructiveTextButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
) {
    Text(
        text = text,
        style = MaterialTheme.typography.bodyLarge,
        color = if (enabled) MaterialTheme.colorScheme.error else MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = modifier
            .clip(Squircle.control())
            .clickable(enabled = enabled, onClick = onClick)
            .padding(horizontal = 14.dp, vertical = 10.dp),
    )
}

/** 主按钮：蓝底白字，G3 圆角，按下缩到 0.98（没有水波纹）。 */
@Composable
fun ApplePrimaryButton(
    text: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
    containerColor: Color = MaterialTheme.colorScheme.primary,
) {
    ApplePressable(
        onClick = onClick,
        enabled = enabled,
        shape = remember { Squircle.control() },
        modifier = modifier,
    ) {
        Box(
            modifier = Modifier
                .fillMaxWidth()
                .heightIn(min = 52.dp)
                .background(if (enabled) containerColor else containerColor.copy(alpha = 0.4f)),
            contentAlignment = Alignment.Center,
        ) {
            Text(
                text,
                style = MaterialTheme.typography.titleMedium,
                color = Color.White,
                fontWeight = FontWeight.SemiBold,
            )
        }
    }
}

/** 状态标签的语义色：低饱和底 + 高饱和字，参考 iOS 的 ok / denied 徽标。 */
enum class PillTone { Ok, Warn, Bad, Neutral }

/**
 * 状态标签：完美胶囊（半径 = 高度一半），
 * 底色饱和度压到 8%~14%，文字取同色系最深的一档，保证小字号也够对比度。
 */
@Composable
fun ApplePill(
    text: String,
    tone: PillTone = PillTone.Neutral,
    modifier: Modifier = Modifier,
    leadingDot: Boolean = true,
) {
    val (bg, fg) = when (tone) {
        PillTone.Ok -> Color(0xFFE3F7E8) to Color(0xFF12833C)
        PillTone.Warn -> Color(0xFFFFF3E0) to Color(0xFFB26A00)
        PillTone.Bad -> Color(0xFFFDE8E6) to Color(0xFFD12B20)
        PillTone.Neutral -> Color(0xFFEFEFF4) to Color(0xFF6E6E73)
    }
    Row(
        modifier = modifier
            .clip(remember { Squircle.capsule() })
            .background(bg)
            .padding(horizontal = 9.dp, vertical = 3.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (leadingDot) {
            Box(Modifier.size(5.dp).clip(CircleShape).background(fg))
            Spacer(Modifier.width(5.dp))
        }
        Text(
            text,
            style = MaterialTheme.typography.labelSmall,
            color = fg,
            fontWeight = FontWeight.SemiBold,
        )
    }
}
