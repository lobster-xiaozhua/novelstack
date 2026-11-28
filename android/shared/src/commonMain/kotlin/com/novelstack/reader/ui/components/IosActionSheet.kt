package com.novelstack.reader.ui.components

import androidx.compose.foundation.background
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
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import com.novelstack.reader.ui.AppleTokens

/** 底部菜单里的一条动作。 */
data class SheetAction(
    val label: String,
    val danger: Boolean = false,
    val onClick: () -> Unit,
)

/**
 * iOS 风格的底部确认菜单。
 *
 * 不用 Material 的 ModalBottomSheet：它带拖拽把手、会贴住屏幕底边只圆上面两个角，
 * 而我们要的是四角一致 G3 的「悬浮卡片 + 独立取消卡」。所以用 Dialog 自己搭，
 * 形状和全站其它卡片复用同一套 Squircle。
 */
@Composable
fun IosActionSheet(
    visible: Boolean,
    title: String,
    message: String = "",
    actions: List<SheetAction>,
    cancelText: String = "取消",
    onDismiss: () -> Unit,
) {
    if (!visible) return
    Dialog(
        onDismissRequest = onDismiss,
        properties = DialogProperties(
            usePlatformDefaultWidth = false,
            dismissOnClickOutside = true,
        ),
    ) {
        Box(
            modifier = Modifier
                .fillMaxSize()
                .background(Color.Black.copy(alpha = 0.4f))
                .clickable(
                    interactionSource = remember { MutableInteractionSource() },
                    indication = null,
                    onClick = onDismiss,
                ),
        ) {
            Column(
                modifier = Modifier
                    .align(Alignment.BottomCenter)
                    .navigationBarsPadding()
                    .padding(horizontal = 8.dp, vertical = 8.dp)
                    .fillMaxWidth(),
                verticalArrangement = Arrangement.spacedBy(8.dp),
            ) {
                // 动作卡：标题 + 说明 + 动作行
                SquircleCard(modifier = Modifier.fillMaxWidth()) {
                    Column {
                        if (title.isNotBlank()) {
                            Column(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .padding(horizontal = 20.dp, vertical = 15.dp),
                                horizontalAlignment = Alignment.CenterHorizontally,
                            ) {
                                Text(
                                    title,
                                    style = MaterialTheme.typography.titleSmall,
                                    fontWeight = FontWeight.SemiBold,
                                    textAlign = TextAlign.Center,
                                )
                                if (message.isNotBlank()) {
                                    Spacer(Modifier.height(4.dp))
                                    Text(
                                        message,
                                        style = MaterialTheme.typography.bodySmall,
                                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                                        textAlign = TextAlign.Center,
                                    )
                                }
                            }
                            HorizontalDivider(
                                thickness = AppleTokens.HAIRLINE_DP.dp,
                                color = MaterialTheme.colorScheme.outlineVariant,
                            )
                        }
                        actions.forEachIndexed { i, action ->
                            Row(
                                modifier = Modifier
                                    .fillMaxWidth()
                                    .clickable {
                                        onDismiss()
                                        action.onClick()
                                    }
                                    .padding(vertical = 16.dp),
                                horizontalArrangement = Arrangement.Center,
                            ) {
                                Text(
                                    action.label,
                                    style = MaterialTheme.typography.titleMedium,
                                    color = if (action.danger) MaterialTheme.colorScheme.error
                                    else MaterialTheme.colorScheme.primary,
                                    fontWeight = if (action.danger) FontWeight.SemiBold else FontWeight.Medium,
                                )
                            }
                            if (i != actions.lastIndex) {
                                HorizontalDivider(
                                    thickness = AppleTokens.HAIRLINE_DP.dp,
                                    color = MaterialTheme.colorScheme.outlineVariant,
                                )
                            }
                        }
                    }
                }

                // 取消卡：和动作卡之间留 8dp，是 iOS 的固定间距
                SquircleCard(modifier = Modifier.fillMaxWidth()) {
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .clickable(onClick = onDismiss)
                            .padding(vertical = 16.dp),
                        horizontalArrangement = Arrangement.Center,
                    ) {
                        Text(
                            cancelText,
                            style = MaterialTheme.typography.titleMedium,
                            fontWeight = FontWeight.SemiBold,
                            color = MaterialTheme.colorScheme.primary,
                        )
                    }
                }
            }
        }
    }
}
