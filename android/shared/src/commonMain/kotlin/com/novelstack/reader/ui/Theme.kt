package com.novelstack.reader.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.sp

/**
 * 苹果风设计令牌。
 *
 * 规则（第十一阶段）：
 *  - 页面底色纯浅灰 #F2F2F7，卡片纯白 #FFFFFF；
 *  - 白卡在灰底上靠 0.5dp 极细边框显形，不用阴影、不用毛玻璃；
 *  - 主标题 #1C1C1E，辅助文字 #8E8E93，强调色 iOS 蓝 #007AFF，危险红 #FF3B30；
 *  - 选中态底色 #E6F0FF + 文字变蓝，替代方形水波纹。
 */
object AppleTokens {
    val PageBg = Color(0xFFF2F2F7)
    val CardBg = Color(0xFFFFFFFF)
    val Label = Color(0xFF1C1C1E)
    val SecondaryLabel = Color(0xFF8E8E93)
    val TertiaryLabel = Color(0xFFC7C7CC)
    val Blue = Color(0xFF007AFF)
    val Red = Color(0xFFFF3B30)
    val Green = Color(0xFF34C759)
    val Orange = Color(0xFFFF9500)
    val SelectionBg = Color(0xFFE6F0FF)

    /** 分割线：60,60,67 @ 12%。 */
    val Hairline = Color(0x1F3C3C43)

    /** 极细边框宽度（dp）：0.5dp 在 xhdpi 以上正好是 1 物理像素级。 */
    const val HAIRLINE_DP = 0.5f

    // 深色模式仍保留层级，但同样不用阴影/毛玻璃。
    val DarkPageBg = Color(0xFF000000)
    val DarkCardBg = Color(0xFF1C1C1E)
    val DarkLabel = Color(0xFFFFFFFF)
    val DarkSecondary = Color(0xFF8E8E93)
    val DarkHairline = Color(0x33EBEBF5)
    val DarkBlue = Color(0xFF0A84FF)
    val DarkSelectionBg = Color(0xFF0A2540)
}

private val LightScheme = lightColorScheme(
    primary = AppleTokens.Blue,
    onPrimary = Color.White,
    secondary = AppleTokens.Blue,
    onSecondary = Color.White,
    background = AppleTokens.PageBg,
    onBackground = AppleTokens.Label,
    surface = AppleTokens.CardBg,
    onSurface = AppleTokens.Label,
    surfaceVariant = AppleTokens.PageBg,
    onSurfaceVariant = AppleTokens.SecondaryLabel,
    outline = Color(0xFFC7C7CC),
    outlineVariant = AppleTokens.Hairline,
    error = AppleTokens.Red,
    onError = Color.White,
)

private val DarkScheme = darkColorScheme(
    primary = AppleTokens.DarkBlue,
    onPrimary = Color.White,
    secondary = AppleTokens.DarkBlue,
    onSecondary = Color.White,
    background = AppleTokens.DarkPageBg,
    onBackground = AppleTokens.DarkLabel,
    surface = AppleTokens.DarkCardBg,
    onSurface = AppleTokens.DarkLabel,
    surfaceVariant = AppleTokens.DarkCardBg,
    onSurfaceVariant = AppleTokens.DarkSecondary,
    outline = Color(0xFF48484A),
    outlineVariant = AppleTokens.DarkHairline,
    error = Color(0xFFFF453A),
    onError = Color.White,
)

/**
 * 排版：SF / 系统字体，标题 SemiBold、正文 Regular。
 * 行高按 iOS 的阅读节奏给（正文 1.45 倍），中文长文不挤。
 */
private val AppleTypography = Typography(
    headlineLarge = TextStyle(fontSize = 30.sp, lineHeight = 36.sp, fontWeight = FontWeight.Bold),
    headlineMedium = TextStyle(fontSize = 24.sp, lineHeight = 30.sp, fontWeight = FontWeight.SemiBold),
    headlineSmall = TextStyle(fontSize = 20.sp, lineHeight = 26.sp, fontWeight = FontWeight.SemiBold),
    titleLarge = TextStyle(fontSize = 18.sp, lineHeight = 24.sp, fontWeight = FontWeight.SemiBold),
    titleMedium = TextStyle(fontSize = 16.sp, lineHeight = 22.sp, fontWeight = FontWeight.Medium),
    titleSmall = TextStyle(fontSize = 14.sp, lineHeight = 20.sp, fontWeight = FontWeight.Medium),
    bodyLarge = TextStyle(fontSize = 16.sp, lineHeight = 23.sp, fontWeight = FontWeight.Normal),
    bodyMedium = TextStyle(fontSize = 15.sp, lineHeight = 21.sp, fontWeight = FontWeight.Normal),
    bodySmall = TextStyle(fontSize = 13.sp, lineHeight = 18.sp, fontWeight = FontWeight.Normal),
    labelLarge = TextStyle(fontSize = 15.sp, lineHeight = 20.sp, fontWeight = FontWeight.Medium),
    labelMedium = TextStyle(fontSize = 13.sp, lineHeight = 18.sp, fontWeight = FontWeight.Medium),
    labelSmall = TextStyle(fontSize = 11.sp, lineHeight = 15.sp, fontWeight = FontWeight.Normal),
)

@Composable
fun NovelTheme(dark: Boolean = isSystemInDarkTheme(), content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = if (dark) DarkScheme else LightScheme,
        typography = AppleTypography,
        content = content,
    )
}

/** 阅读区配色（与 App 主题解耦，读者可单独选择纸张色）。 */
data class ReaderPalette(val bg: Color, val fg: Color, val dim: Color)

fun readerPalette(key: String): ReaderPalette = when (key) {
    "paper" -> ReaderPalette(Color(0xFFF7F2E8), Color(0xFF2A2723), Color(0xFF7A7368))
    "sepia" -> ReaderPalette(Color(0xFFEFE2CC), Color(0xFF4A3A28), Color(0xFF8A7355))
    "mint" -> ReaderPalette(Color(0xFFE4F0E8), Color(0xFF1E3528), Color(0xFF5C7768))
    else -> ReaderPalette(Color(0xFF0D1117), Color(0xFFD6DEE8), Color(0xFF7D8A99))
}

/** 主题 key（auto/light/dark/eye）→ App 是否使用暗色。 */
fun isDarkTheme(theme: String, systemDark: Boolean): Boolean = when (theme) {
    "dark" -> true
    "light", "eye" -> false
    else -> systemDark // auto
}

/** 主题 key + 系统明暗 → 阅读区纸张色。 */
fun readerPaletteFor(theme: String, systemDark: Boolean): ReaderPalette = when (theme) {
    "dark" -> readerPalette("dark")
    "light" -> readerPalette("paper")
    "eye" -> readerPalette("sepia") // 护眼：黄底
    else -> if (systemDark) readerPalette("dark") else readerPalette("paper")
}
