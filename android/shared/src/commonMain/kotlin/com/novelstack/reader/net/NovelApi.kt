package com.novelstack.reader.net

import com.novelstack.reader.model.ApiError
import com.novelstack.reader.model.ApiException
import com.novelstack.reader.model.AppVersion
import com.novelstack.reader.model.AudioManifest
import com.novelstack.reader.model.DeviceAuth
import io.ktor.http.encodeURLParameter
import com.novelstack.reader.model.SyncProgressResponse
import com.novelstack.reader.model.WebGrantDto
import com.novelstack.reader.model.DiscoverResponse
import com.novelstack.reader.model.FavoriteRequest
import com.novelstack.reader.model.FavoriteResponse
import com.novelstack.reader.model.HistoryResponse
import com.novelstack.reader.model.LanInfo
import com.novelstack.reader.model.LanPairRequest
import com.novelstack.reader.model.LatestHash
import com.novelstack.reader.model.LoginRequest
import com.novelstack.reader.model.PrefetchRequest
import com.novelstack.reader.model.PrefetchResponse
import com.novelstack.reader.model.PrepareResponse
import com.novelstack.reader.model.ProgressDto
import com.novelstack.reader.model.ProgressRequest
import com.novelstack.reader.model.ProgressResponse
import com.novelstack.reader.model.ReadingStats
import com.novelstack.reader.model.SyncManifest
import io.ktor.client.HttpClient
import io.ktor.client.request.get
import io.ktor.client.request.header
import io.ktor.client.request.patch
import io.ktor.client.request.post
import io.ktor.client.request.put
import io.ktor.client.request.setBody
import io.ktor.client.statement.HttpResponse
import io.ktor.client.statement.bodyAsChannel
import io.ktor.client.statement.bodyAsText
import io.ktor.http.ContentType
import io.ktor.http.contentType
import io.ktor.http.isSuccess
import io.ktor.utils.io.toByteArray
import kotlinx.serialization.json.Json

/**
 * 服务端 API 客户端。
 *
 * 凭证处理：
 *  - device token 只放在 Authorization 头，不拼进 URL、不写日志；
 *  - 响应体一律按「密文/元数据」处理，本类不做任何解密；
 *  - 401 由上层清除本地 token 并回到配对页。
 */
class NovelApi(
    private val client: HttpClient,
    private val json: Json,
) {
    @Volatile
    private var baseUrl: String = ""

    @Volatile
    private var token: String? = null

    /**
     * 配置主地址与凭证。
     *
     * 第十阶段：地址统一由 [ApiEndpointManager] 管理（含内网/公网双节点）。
     * 这里保留 baseUrl 字段作为「manager 尚未安装」时的回退，并同步 token 到 manager。
     */
    fun configure(server: String, token: String?) {
        val normalized = normalizeServer(server)
        baseUrl = normalized
        this.token = token
        val mgr = ApiEndpointManager
        if (mgr.hasConfig()) {
            // 保留已有内网地址与探测结果，只替换主地址与 token。
            mgr.configure(mgr.primaryUrl().ifBlank { normalized }, mgr.localUrl(), token.orEmpty())
        }
    }

    /** 当前实际使用的根地址（由双节点探测决定内网或公网）。 */
    fun currentBaseUrl(): String {
        val active = ApiEndpointManager.activeBaseUrl()
        return active.ifBlank { baseUrl }
    }

    fun hasToken(): Boolean = !token.isNullOrBlank()

    suspend fun loginDevice(server: String, req: LoginRequest): DeviceAuth {
        val url = normalizeServer(server) + "/api/device/token"
        val resp = client.post(url) {
            contentType(ContentType.Application.Json)
            setBody(json.encodeToString(LoginRequest.serializer(), req))
        }
        return json.decodeFromString(DeviceAuth.serializer(), resp.textOrThrow())
    }

    // ---- 第十阶段：局域网发现与短码配对 ----

    /** 用 6 位短码换取 device token（无需 session，短码自带 IP 绑定与限流）。 */
    suspend fun lanPair(server: String, req: LanPairRequest): DeviceAuth {
        val url = normalizeServer(server) + "/api/lan/pair"
        val resp = client.post(url) {
            contentType(ContentType.Application.Json)
            setBody(json.encodeToString(LanPairRequest.serializer(), req))
        }
        return json.decodeFromString(DeviceAuth.serializer(), resp.textOrThrow())
    }

    /** 读取局域网发现信息（无需鉴权，只含地址）。失败返回 null。 */
    suspend fun lanInfo(server: String): LanInfo? = runCatching {
        val resp = client.get(normalizeServer(server) + "/api/lan/info")
        json.decodeFromString(LanInfo.serializer(), resp.textOrThrow())
    }.getOrNull()

    suspend fun manifest(since: Long): SyncManifest {
        val resp = client.get("${currentBaseUrl()}/api/sync/manifest?since=$since") { auth() }
        return json.decodeFromString(SyncManifest.serializer(), resp.textOrThrow())
    }

    suspend fun prefetch(bookId: String, indexes: List<Int>): PrefetchResponse {
        val resp = client.post("${currentBaseUrl()}/api/books/$bookId/chapters/prefetch") {
            auth()
            contentType(ContentType.Application.Json)
            setBody(json.encodeToString(PrefetchRequest.serializer(), PrefetchRequest(indexes)))
        }
        return json.decodeFromString(PrefetchResponse.serializer(), resp.textOrThrow())
    }

    suspend fun putProgress(bookId: String, req: ProgressRequest): ProgressResponse {
        val resp = client.put("${currentBaseUrl()}/api/books/$bookId/progress") {
            auth()
            contentType(ContentType.Application.Json)
            setBody(json.encodeToString(ProgressRequest.serializer(), req))
        }
        return json.decodeFromString(ProgressResponse.serializer(), resp.textOrThrow())
    }

    /** 拉取增量进度（since 为空表示全量）。 */
    suspend fun syncProgress(since: String?): SyncProgressResponse {
        val q = if (since.isNullOrBlank()) "" else "?since=" + since.encodeURLParameter()
        val resp = client.get("${currentBaseUrl()}/api/sync/progress$q") { auth() }
        return json.decodeFromString(SyncProgressResponse.serializer(), resp.textOrThrow())
    }

    /**
     * 申请 WebView 一次性登录票据（60 秒、单次）。
     * 服务端返回的 url 形如 `https://host/admin/login?grant=...`。
     */
    suspend fun webGrant(): WebGrantDto {
        val resp = client.post("${currentBaseUrl()}/api/device/web-grant") {
            auth()
            contentType(ContentType.Application.Json)
            setBody("{}")
        }
        return json.decodeFromString(WebGrantDto.serializer(), resp.textOrThrow())
    }

    suspend fun getProgress(bookId: String): ProgressDto? {
        val resp = client.get("${currentBaseUrl()}/api/books/$bookId/progress") { auth() }
        if (resp.status.value == 404) return null
        return json.decodeFromString(ProgressDto.serializer(), resp.textOrThrow())
    }

    // ---- 第八阶段：发现 / 足迹 / 收藏 / 版本 ----

    suspend fun history(): HistoryResponse {
        val resp = client.get("${currentBaseUrl()}/api/history") { auth() }
        return json.decodeFromString(HistoryResponse.serializer(), resp.textOrThrow())
    }

    suspend fun discover(): DiscoverResponse {
        val resp = client.get("${currentBaseUrl()}/api/discover") { auth() }
        return json.decodeFromString(DiscoverResponse.serializer(), resp.textOrThrow())
    }

    /** 设置/取消收藏，返回服务端回显的状态。 */
    suspend fun setFavorite(bookId: String, favorite: Boolean): Boolean {
        val resp = client.patch("${currentBaseUrl()}/api/books/$bookId/favorite") {
            auth()
            contentType(ContentType.Application.Json)
            setBody(json.encodeToString(FavoriteRequest.serializer(), FavoriteRequest(favorite)))
        }
        val r = json.decodeFromString(FavoriteResponse.serializer(), resp.textOrThrow())
        return r.isFavorite
    }

    /** 阅读统计（今日/累计/连续天数/近 90 天）。 */
    suspend fun readingStats(): ReadingStats {
        val resp = client.get("${currentBaseUrl()}/api/stats/reading") { auth() }
        return json.decodeFromString(ReadingStats.serializer(), resp.textOrThrow())
    }

    suspend fun appVersion(): AppVersion {
        val resp = client.get("${currentBaseUrl()}/api/app/version") { auth() }
        return json.decodeFromString(AppVersion.serializer(), resp.textOrThrow())
    }

    /** 服务端未配置哈希时返回 null（客户端应提示错误，而不是安装未校验的包）。 */
    suspend fun latestHash(): LatestHash? {
        val resp = client.get("${currentBaseUrl()}/api/app/version/latest-hash") { auth() }
        if (resp.status.value == 404) return null
        return json.decodeFromString(LatestHash.serializer(), resp.textOrThrow())
    }

    /**
     * 上送遥测批次。body 已在平台层 gzip 压缩；服务端按 Content-Encoding 解压。
     * 非 2xx 抛 ApiException，由 WorkManager 决定重试。
     */
    suspend fun uploadTelemetry(gzipBody: ByteArray) {
        val resp = client.post("${currentBaseUrl()}/api/telemetry/batch") {
            auth()
            contentType(ContentType.Application.Json)
            header("Content-Encoding", "gzip")
            setBody(gzipBody)
        }
        // 读空响应体，确保连接可复用；状态码由 textOrThrow 校验。
        resp.textOrThrow()
    }

    suspend fun audioManifest(bookId: String, chapterIndex: Int): AudioManifest {
        val resp = client.get("${currentBaseUrl()}/api/books/$bookId/chapters/$chapterIndex/audio") { auth() }
        return json.decodeFromString(AudioManifest.serializer(), resp.textOrThrow())
    }

    suspend fun prepareAudio(bookId: String, chapterIndex: Int, speed: Float): PrepareResponse {
        val resp = client.post(
            "${currentBaseUrl()}/api/books/$bookId/chapters/$chapterIndex/audio/prepare?speed=$speed",
        ) { auth() }
        return json.decodeFromString(PrepareResponse.serializer(), resp.textOrThrow())
    }

    /** 下载 NSAC 加密音频文件原文（不解密、不落盘）。 */
    suspend fun segmentBytes(key: String): ByteArray {
        val resp = client.get("${currentBaseUrl()}/api/tts/segments/$key") { auth() }
        if (!resp.status.isSuccess()) throw errorOf(resp)
        return resp.bodyAsChannel().toByteArray()
    }

    private fun io.ktor.client.request.HttpRequestBuilder.auth() {
        token?.let { header("Authorization", "Bearer $it") }
        header("Accept", "application/json")
    }

    private suspend fun HttpResponse.textOrThrow(): String {
        val text = bodyAsText()
        if (!status.isSuccess()) throw errorOf(this, text)
        return text
    }

    private suspend fun errorOf(resp: HttpResponse, text: String? = null): ApiException {
        val raw = text ?: runCatching { resp.bodyAsText() }.getOrDefault("")
        val msg = runCatching { json.decodeFromString(ApiError.serializer(), raw).error }
            .getOrNull()
            ?.takeIf { it.isNotBlank() }
            ?: raw.take(200)
        return ApiException(resp.status.value, msg)
    }

    companion object {
        /**
         * 规范化服务端地址：
         *  - 去掉尾部斜杠；
         *  - 没有 scheme 时，局域网/模拟器地址补 http://，其余补 https://。
         * 模拟器访问宿主机固定为 10.0.2.2。
         */
        fun normalizeServer(input: String): String {
            var s = input.trim()
            if (s.isEmpty()) return "https://localhost"
            if (s.startsWith("http://") || s.startsWith("https://")) {
                return s.trimEnd('/')
            }
            s = s.trimEnd('/')
            val host = s.substringBefore(':').substringBefore('/')
            val second = host.split('.').getOrNull(1)?.toIntOrNull()
            val local = host == "localhost" || host == "127.0.0.1" || host == "10.0.2.2" ||
                host.startsWith("192.168.") || host.startsWith("10.") ||
                (host.startsWith("172.") && second != null && second in 16..31)
            return (if (local) "http://" else "https://") + s
        }
    }
}
