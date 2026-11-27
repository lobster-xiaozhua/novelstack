package com.novelstack.reader.net

import com.novelstack.reader.telemetry.Telemetry
import com.novelstack.reader.telemetry.TelemetryKind
import io.ktor.client.HttpClient
import io.ktor.client.engine.okhttp.OkHttp
import io.ktor.client.plugins.HttpSend
import io.ktor.client.plugins.HttpTimeout
import io.ktor.client.plugins.plugin

/**
 * 网络客户端。
 *
 * 刻意不安装 Logging 插件：请求/响应体里可能有密文、token 与正文片段，
 * 都不能进 logcat 或崩溃报告。
 *
 * 第九阶段：只记录「路径 + 方法 + 状态码 + 耗时」，
 * 不读 body、不记 query（可能含书名）、不记任何 header（可能含 Authorization）。
 */
fun createHttpClient(): HttpClient {
    val client = HttpClient(OkHttp) {
        expectSuccess = false
        install(HttpTimeout) {
            connectTimeoutMillis = 10_000
            requestTimeoutMillis = 60_000
            socketTimeoutMillis = 60_000
        }
        engine {
            config {
                retryOnConnectionFailure(true)
            }
        }
    }
    client.plugin(HttpSend).intercept { request ->
        val started = Telemetry.now()
        // 超时计数：连续 3 次超时进入离线模式（4xx/5xx 不算，服务器还活着）。
        val call = try {
            execute(request)
        } catch (t: Throwable) {
            NetworkCircuit.onFailure(t)
            throw t
        }
        NetworkCircuit.onSuccess()
        runCatching {
            // 上传遥测本身的请求不记录，否则每轮上传都会给自己加一条，形成噪音。
            // 只取路径段，天然排除 query（可能含书名/关键词）与 host。
            val path = "/" + request.url.pathSegments.joinToString("/")
            if (!path.endsWith("/api/telemetry/batch")) {
                Telemetry.record(
                    kind = TelemetryKind.API,
                    name = path,
                    durationMs = Telemetry.now() - started,
                    context = mapOf(
                        "method" to request.method.value,
                        "status" to call.response.status.value.toString(),
                    ),
                )
            }
        }
        call
    }
    return client
}
