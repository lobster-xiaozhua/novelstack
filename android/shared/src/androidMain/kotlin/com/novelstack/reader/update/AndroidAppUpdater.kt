package com.novelstack.reader.update

import android.content.Context
import android.content.Intent
import androidx.core.content.FileProvider
import io.ktor.client.HttpClient
import io.ktor.client.engine.okhttp.OkHttp
import io.ktor.client.plugins.HttpTimeout
import io.ktor.client.request.get
import io.ktor.client.request.header
import io.ktor.client.statement.bodyAsChannel
import io.ktor.http.isSuccess
import io.ktor.utils.io.readAvailable
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.withContext
import java.io.ByteArrayOutputStream
import java.io.File

/**
 * Android 平台更新器实现。
 *
 * 流程：
 *  1. 并发对每个镜像发起 Range 请求只取前 100KB，记录耗时，选最快者；
 *  2. 从最快镜像下载完整 APK（进度回调）；
 *  3. 纯 Kotlin SHA-256 校验（复用 commonMain 的 [Sha256]，逻辑可单测）；
 *  4. 写入 cacheDir/updates，供 FileProvider 拉起系统安装器。
 *
 * 网络层复用项目已有的 Ktor（okhttp 引擎），不额外引入第三方 HTTP 客户端。
 * 安全：哈希不符绝不落盘可安装文件，直接抛异常；安装必须经 FileProvider（Android 7.0+）。
 */
class AndroidAppUpdater(private val context: Context) : AppUpdater {

    private val http = HttpClient(OkHttp) {
        install(HttpTimeout) {
            connectTimeoutMillis = 15_000
            requestTimeoutMillis = 120_000
        }
    }

    companion object {
        private const val PROBE_BYTES = 100 * 1024
    }

    override suspend fun downloadAndVerify(
        mirrors: List<String>,
        expectedSha256: String,
        onProgress: (Float) -> Unit,
    ): String = withContext(Dispatchers.IO) {
        require(mirrors.isNotEmpty()) { "服务端未提供可用镜像" }
        val best = selectFastest(mirrors)
            ?: throw IllegalStateException("所有镜像均不可用，请稍后重试")
        val bytes = download(best, onProgress)
        if (!UpdatePlanner.verify(bytes, expectedSha256)) {
            throw IllegalStateException("SHA-256 校验失败，已拒绝安装")
        }
        val dir = File(context.cacheDir, "updates").apply { mkdirs() }
        val out = File(dir, "novelstack-${System.currentTimeMillis()}.apk")
        out.writeBytes(bytes)
        out.absolutePath
    }

    /** 并发测速，返回最快镜像；全部失败返回 null。 */
    private suspend fun selectFastest(mirrors: List<String>): String? = coroutineScope {
        val probes = mirrors.map { url ->
            async(Dispatchers.IO) { url to probe(url) }
        }.map { it.await() }
        UpdatePlanner.pickFastest(probes)
    }

    /** 只读前 100KB 测耗时；失败返回 0（被 [UpdatePlanner.pickFastest] 忽略）。 */
    private suspend fun probe(url: String): Long = try {
        val start = System.nanoTime()
        val resp = http.get(url) { header("Range", "bytes=0-${PROBE_BYTES - 1}") }
        if (resp.status.value != 200 && resp.status.value != 206) {
            0L
        } else {
            val ch = resp.bodyAsChannel()
            val buf = ByteArray(16 * 1024)
            var read = 0
            while (read < PROBE_BYTES) {
                val n = ch.readAvailable(buf, 0, buf.size)
                if (n <= 0) break
                read += n
            }
            (System.nanoTime() - start) / 1_000_000
        }
    } catch (_: Throwable) {
        0L
    }

    private suspend fun download(url: String, onProgress: (Float) -> Unit): ByteArray {
        val resp = http.get(url)
        if (!resp.status.isSuccess()) throw IllegalStateException("下载失败：HTTP ${resp.status.value}")
        val total = resp.headers["Content-Length"]?.toLongOrNull() ?: -1L
        val ch = resp.bodyAsChannel()
        val out = ByteArrayOutputStream(if (total > 0) total.toInt() else 1 shl 20)
        val buf = ByteArray(64 * 1024)
        var readTotal = 0L
        while (true) {
            val n = ch.readAvailable(buf, 0, buf.size)
            if (n < 0) break
            out.write(buf, 0, n)
            readTotal += n
            if (total > 0) onProgress((readTotal.toFloat() / total).coerceIn(0f, 1f))
        }
        onProgress(1f)
        return out.toByteArray()
    }

    override fun install(apkPath: String) {
        val file = File(apkPath)
        if (!file.exists()) throw IllegalStateException("安装包不存在")
        val authority = "${context.packageName}.fileprovider"
        val uri = FileProvider.getUriForFile(context, authority, file)
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        context.startActivity(intent)
    }
}
