package com.novelstack.reader.app

/**
 * iOS 侧暂未接入构建期版本注入（iOS 不在第五至八阶段验收范围），
 * 返回占位版本，保证 commonMain 的 expect 在 macOS 上可编译。
 */
actual fun appVersionName(): String = "0.1.0"
