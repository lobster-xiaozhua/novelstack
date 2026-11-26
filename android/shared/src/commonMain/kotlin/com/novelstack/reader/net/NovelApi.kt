package com.novelstack.reader.net

import com.novelstack.reader.model.ApiError
import com.novelstack.reader.model.ApiException
import com.novelstack.reader.model.AudioManifest
import com.novelstack.reader.model.DeviceAuth
import com.novelstack.reader.model.LoginRequest
import com.novelstack.reader.model.PrefetchRequest
import com.novelstack.reader.model.PrefetchResponse
import com.novelstack.reader.model.PrepareResponse
import com.novelstack.reader.model.ProgressDto
import com.novelstack.reader.model.ProgressRequest
import com.novelstack.reader.model.ProgressResponse
import com.novelstack.reader.model.SyncManifest
import io.ktor.client.HttpClient
import io.ktor.client.request.get
import io.ktor.client.request.header
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

    fun configure(server: String, token: String?) {
        baseUrl = normalizeServer(server)
        this.token = token
    }

    fun currentBaseUrl(): String = baseUrl

    fun hasToken(): Boolean = !token.isNullOrBlank()

    suspend fun loginDevice(server: String, req: LoginRequest): DeviceAuth {
        val url = normalizeServer(server) + "/api/device/token"
        val resp = client.post(url) {
            contentType(ContentType.Application.Json)
            setBody(json.encodeToString(LoginRequest.serializer(), req))
        }
        return json.decodeFromString(DeviceAuth.serializer(), resp.textOrThrow())
    }

    suspend fun manifest(since: Long): SyncManifest {
        val resp = client.get("$baseUrl/api/sync/manifest?since=$since") { auth() }
        return json.decodeFromString(SyncManifest.serializer(), resp.textOrThrow())
    }

    suspend fun prefetch(bookId: String, indexes: List<Int>): PrefetchResponse {
        val resp = client.post("$baseUrl/api/books/$bookId/chapters/prefetch") {
            auth()
            contentType(ContentType.Application.Json)
            setBody(json.encodeToString(PrefetchRequest.serializer(), PrefetchRequest(indexes)))
        }
        return json.decodeFromString(PrefetchResponse.serializer(), resp.textOrThrow())
    }

    suspend fun putProgress(bookId: String, req: ProgressRequest): ProgressResponse {
        val resp = client.put("$baseUrl/api/books/$bookId/progress") {
            auth()
            contentType(ContentType.Application.Json)
            setBody(json.encodeToString(ProgressRequest.serializer(), req))
        }
        return json.decodeFromString(ProgressResponse.serializer(), resp.textOrThrow())
    }

    suspend fun getProgress(bookId: String): ProgressDto? {
        val resp = client.get("$baseUrl/api/books/$bookId/progress") { auth() }
        if (resp.status.value == 404) return null
        return json.decodeFromString(ProgressDto.serializer(), resp.textOrThrow())
    }

    suspend fun audioManifest(bookId: String, chapterIndex: Int): AudioManifest {
        val resp = client.get("$baseUrl/api/books/$bookId/chapters/$chapterIndex/audio") { auth() }
        return json.decodeFromString(AudioManifest.serializer(), resp.textOrThrow())
    }

    suspend fun prepareAudio(bookId: String, chapterIndex: Int, speed: Float): PrepareResponse {
        val resp = client.post(
            "$baseUrl/api/books/$bookId/chapters/$chapterIndex/audio/prepare?speed=$speed",
        ) { auth() }
        return json.decodeFromString(PrepareResponse.serializer(), resp.textOrThrow())
    }

    /** 下载 NSAC 加密音频文件原文（不解密、不落盘）。 */
    suspend fun segmentBytes(key: String): ByteArray {
        val resp = client.get("$baseUrl/api/tts/segments/$key") { auth() }
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
