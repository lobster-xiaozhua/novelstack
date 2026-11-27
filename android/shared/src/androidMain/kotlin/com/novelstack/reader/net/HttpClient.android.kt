package com.novelstack.reader.net

import com.novelstack.reader.telemetry.Telemetry
import com.novelstack.reader.telemetry.TelemetryKind
import io.ktor.client.HttpClient
import io.ktor.client.engine.okhttp.OkHttp
import io.ktor.client.plugins.HttpSend
import io.ktor.client.plugins.HttpTimeout
import io.ktor.client.plugins.plugin
import io.ktor.http.URLBuilder
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.OkHttpClient
import okhttp3.Request
import java.util.concurrent.TimeUnit

/**
 * 网络客户端。
 *
 * 刻意不安装 Logging 插件：请求/响应体里可能有密文、token 与正文片段，
 * 都不能进 logcat 或崩溃报告。
 *
 * 第九阶段：只记录「路径 + 方法 + 状态码 + 耗时」，
 * 不读 body、不记 query（可能含书名）、不记任何 header（可能含 Authorization）。
 *
 * 第十阶段：OkHttp 拦截器在每次请求前优先探测 local_url/healthz（500ms），
 * 内网不通则无缝降级到公网。探测结果由 [ApiEndpointManager] 带 TTL 缓存，
 * 并直接改写本次请求的 host/port，实现「无缝」而非「下一请求才生效」。
 */
fun createHttpClient(): HttpClient {
    // 独立探测客户端：连接/读写都压到 500ms，避免内网探测拖慢业务请求。
    // 它不经过 Ktor 拦截器（裸 OkHttp），因此不会递归触发探测。
    val probeClient = OkHttpClient.Builder()
        .connectTimeout(500, TimeUnit.MILLISECONDS)
        .readTimeout(500, TimeUnit.MILLISECONDS)
        .writeTimeout(500, TimeUnit.MILLISECONDS)
        .retryOnConnectionFailure(false)
        .build()

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
        // 1) 请求前探测内网并更新缓存（TTL 内跳过）。
        maybeProbeLocal(probeClient)

        // 2) 按最新探测结果改写业务请求的 host/port（引导类请求保持原样）。
        rewriteHostIfNeeded(request.url)

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

/**
 * 探测内网可达性（500ms 超时），结果写入 [ApiEndpointManager]（带 TTL）。
 *
 * 用一个轻量 OkHttp 客户端同步探测，放到 IO 调度器执行，避免阻塞主线程。
 */
private suspend fun maybeProbeLocal(probeClient: OkHttpClient) {
    val mgr = ApiEndpointManager
    val local = mgr.localUrl()
    if (local.isBlank()) return
    val now = System.currentTimeMillis()
    if (now - mgr.snapshot().localProbedAt < ApiEndpointManager.PROBE_TTL_MS) return

    val healthUrl = buildHealthUrl(local)
    val reachable = withContext(Dispatchers.IO) {
        runCatching {
            val req = Request.Builder().url(healthUrl).get().build()
            probeClient.newCall(req).execute().use { resp -> resp.isSuccessful }
        }.getOrDefault(false)
    }
    mgr.updateLocalProbe(reachable)
}

/**
 * 若当前活动节点与请求 URL 的 host 不一致，则改写 URL。
 *
 * 跳过「引导类」请求：登录、LAN 配对、LAN 信息——它们打向用户显式指定的地址，
 * 不能被自动降级逻辑改写（否则配对时连错服务器）。
 */
private fun rewriteHostIfNeeded(url: URLBuilder) {
    val path = "/" + url.pathSegments.joinToString("/")
    if (!shouldRewrite(path)) return
    val active = ApiEndpointManager.activeBaseUrl()
    if (active.isBlank()) return
    runCatching {
        val target = URLBuilder(active)
        url.protocol = target.protocol
        url.host = target.host
        url.port = target.port
    }
}

private fun shouldRewrite(path: String): Boolean {
    if (!path.startsWith("/api/")) return false
    return when (path) {
        "/api/device/token", "/api/lan/pair", "/api/lan/info" -> false
        else -> true
    }
}

/** 拼 /healthz：local 可能带 http/https 与路径，统一去掉尾部斜杠再追加。 */
private fun buildHealthUrl(local: String): String {
    val base = local.trim().trimEnd('/')
    return if (base.endsWith("/healthz")) base else "$base/healthz"
}
