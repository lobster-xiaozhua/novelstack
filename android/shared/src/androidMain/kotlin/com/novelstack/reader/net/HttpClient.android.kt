package com.novelstack.reader.net

import io.ktor.client.HttpClient
import io.ktor.client.engine.okhttp.OkHttp
import io.ktor.client.plugins.HttpTimeout

/**
 * 网络客户端。
 *
 * 刻意不安装 Logging 插件：请求/响应体里可能有密文、token 与正文片段，
 * 都不能进 logcat 或崩溃报告。
 */
fun createHttpClient(): HttpClient = HttpClient(OkHttp) {
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
