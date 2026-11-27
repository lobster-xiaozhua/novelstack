package com.novelstack.reader.telemetry

import java.io.ByteArrayOutputStream
import java.util.zip.GZIPOutputStream

actual fun gzipCompress(data: ByteArray): ByteArray {
    val out = ByteArrayOutputStream(data.size / 2 + 64)
    GZIPOutputStream(out).use { it.write(data) }
    return out.toByteArray()
}
