package com.novelstack.reader.ui

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Typography
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.graphics.Color

private val DarkScheme = darkColorScheme(
    primary = Color(0xFF8FB4D9),
    onPrimary = Color(0xFF0B1620),
    secondary = Color(0xFF9BB7A8),
    background = Color(0xFF0E1116),
    onBackground = Color(0xFFDCE3EC),
    surface = Color(0xFF161C24),
    onSurface = Color(0xFFDCE3EC),
    surfaceVariant = Color(0xFF1E2630),
    onSurfaceVariant = Color(0xFFA9B6C6),
    outline = Color(0xFF38424F),
    error = Color(0xFFE08A8A),
)

private val LightScheme = lightColorScheme(
    primary = Color(0xFF2F5D8A),
    onPrimary = Color.White,
    secondary = Color(0xFF3F6B57),
    background = Color(0xFFF7F6F3),
    onBackground = Color(0xFF1B1E22),
    surface = Color(0xFFFFFFFF),
    onSurface = Color(0xFF1B1E22),
    surfaceVariant = Color(0xFFEDEAE4),
    onSurfaceVariant = Color(0xFF4A5058),
    outline = Color(0xFFC9C4BB),
)

@Composable
fun NovelTheme(dark: Boolean = isSystemInDarkTheme(), content: @Composable () -> Unit) {
    MaterialTheme(
        colorScheme = if (dark) DarkScheme else LightScheme,
        typography = Typography(),
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
