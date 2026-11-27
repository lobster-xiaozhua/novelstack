package com.novelstack.reader.telemetry

/**
 * iOS 侧暂不参与遥测上传（不在验收范围），直接返回原数据。
 * 由于 iOS 端尚未配置上传任务，这个 actual 只用于保证 commonMain 可编译。
 */
actual fun gzipCompress(data: ByteArray): ByteArray = data
