package com.novelstack.reader.app

import kotlin.test.Test
import kotlin.test.assertTrue

/**
 * 版本注入的最小护栏：只断言格式，不硬编码具体值。
 * 硬编码 0.1.0 会让 CI 打 tag 构建（versionName=0.2.1）时测试误报，
 * 而这里要防的是「注入失效退回空串 / 占位符」这类问题。
 */
class AppVersionTest {

    @Test
    fun versionIsSemverLike() {
        val v = appVersionName()
        assertTrue(v.isNotBlank(), "版本号不能为空，构建期 APP_VERSION 注入可能失效")
        assertTrue(
            Regex("""^\d+\.\d+\.\d+$""").matches(v),
            "版本号应为 X.Y.Z，实际为 $v",
        )
    }

    @Test
    fun stateExposesSameVersion() {
        // 设置页展示与更新比较都读 AppState.APP_VERSION，必须与注入值一致
        assertTrue(AppState.APP_VERSION == appVersionName())
    }
}
