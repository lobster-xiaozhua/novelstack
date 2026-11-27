package com.novelstack.reader.net

import com.novelstack.reader.model.LanPairingPayload
import io.ktor.util.decodeBase64String
import io.ktor.util.encodeBase64
import kotlinx.serialization.Serializable
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.jsonObject

/**
 * EndpointConfig 是持久化的服务端地址配置。
 *
 * 设计要点：
 *  - primaryUrl 是公网地址（必填），localUrl 是内网地址（可选）；
 *  - deviceToken 也放这里，是为了「一键切换服务器」时能原子替换地址与凭证，
 *    避免切到新服务器却带着旧 token 导致一串 401；
 *  - 本对象不打印、不落日志，token 只在内存与本地配置中出现。
 */
@Serializable
data class EndpointConfig(
    val primaryUrl: String = "",
    val localUrl: String = "",
    val deviceToken: String = "",
    /** 最近一次探测到内网可用时间（毫秒 epoch），0 表示从未探测。 */
    val localProbedAt: Long = 0L,
    /** 最近一次探测结果：true=内网可用，false=不可用。 */
    val localReachable: Boolean = false,
)

/**
 * 剪贴板导入载荷（JSON 或 base64(JSON)）。字段兼容常见写法：
 *   {"url":"https://api.xxx.com","local_url":"http://192.168.1.100:8443","token":"nsd_..."}
 *   {"primary_url":"...","local_url":"...","device_token":"..."}
 *
 * 也支持 LAN 配对二维码载荷：{"v":1,"endpoint":"http://192.168.1.5:8080","code":"123456"}
 * 这种载荷由 [LanPairingPayload] 解析，走配对流程而非直接切换。
 */
@Serializable
data class EndpointImport(
    val url: String = "",
    val primaryUrl: String = "",
    val localUrl: String = "",
    val token: String = "",
    val deviceToken: String = "",
    val name: String = "",
) {
    fun primary(): String = primaryUrl.ifBlank { url }
    fun resolvedToken(): String = deviceToken.ifBlank { token }
}

/**
 * 全局单例：管理「动态根地址」与服务器切换。
 *
 * 禁止任何地方硬编码 API 域名——所有请求都通过 [activeBaseUrl] 取地址。
 * 双节点策略：
 *  - 优先探测 localUrl/healthz（500ms 超时）；
 *  - 内网不可用则无缝降级到 primaryUrl；
 *  - 探测结果带 TTL 缓存，避免每个请求都探测一次造成延迟。
 */
object ApiEndpointManager {
    /** 内网探测结果缓存有效期：10 秒。 */
    const val PROBE_TTL_MS = 10_000L

    private var store: EndpointStore? = null
    private var config: EndpointConfig = EndpointConfig()
    private var json: Json = Json { ignoreUnknownKeys = true }

    /** 由 DI 在进程启动时注入一次。 */
    fun install(store: EndpointStore, json: Json) {
        this.store = store
        this.json = json
        this.config = runCatching { store.load() }.getOrDefault(EndpointConfig())
    }

    fun primaryUrl(): String = config.primaryUrl
    fun localUrl(): String = config.localUrl
    fun deviceToken(): String = config.deviceToken
    fun snapshot(): EndpointConfig = config

    fun hasConfig(): Boolean = config.primaryUrl.isNotBlank()

    /** 当前应使用的根地址（不含末尾斜杠）。 */
    fun activeBaseUrl(): String {
        val local = config.localUrl
        val primary = config.primaryUrl
        if (local.isBlank()) return primary.trimEnd('/')
        if (primary.isBlank()) return local.trimEnd('/')
        val fresh = System.currentTimeMillis() - config.localProbedAt < PROBE_TTL_MS
        return if (fresh && config.localReachable) local.trimEnd('/') else primary.trimEnd('/')
    }

    /** 是否正处于内网地址。 */
    fun isLocalActive(): Boolean {
        val local = config.localUrl
        if (local.isBlank()) return false
        return activeBaseUrl() == local.trimEnd('/')
    }

    /**
     * 由网络拦截器在请求前调用，探测内网可达性。
     * 探测函数由平台层提供（避免 commonMain 依赖具体 HTTP 引擎），
     * 返回 true 表示 localUrl/healthz 在 500ms 内返回成功。
     */
    fun updateLocalProbe(reachable: Boolean) {
        val now = System.currentTimeMillis()
        if (config.localReachable == reachable && now - config.localProbedAt < PROBE_TTL_MS) return
        config = config.copy(localProbedAt = now, localReachable = reachable)
        persist()
    }

    /** 保存/切换服务器配置。传空 primaryUrl 表示清除。 */
    fun configure(primary: String, local: String, token: String) {
        config = EndpointConfig(
            primaryUrl = NovelApi.normalizeServer(primary),
            localUrl = if (local.isBlank()) "" else NovelApi.normalizeServer(local),
            deviceToken = token,
            localProbedAt = 0L,
            localReachable = false,
        )
        persist()
    }

    /** 仅更新 token（配对成功后）。 */
    fun setToken(token: String) {
        config = config.copy(deviceToken = token)
        persist()
    }

    /** 清除全部配置（退出登录 / 清除数据）。 */
    fun clear() {
        config = EndpointConfig()
        store?.clear()
    }

    /**
     * 解析剪贴板文本：支持原始 JSON、base64(JSON)、以及带前后空白/换行。
     * 返回 null 表示不是合法配置载荷。
     */
    fun parseImport(raw: String): EndpointImport? {
        val trimmed = raw.trim()
        if (trimmed.isBlank()) return null
        val candidates = buildList {
            add(trimmed)
            // base64 解码尝试（失败就跳过）
            runCatching { trimmed.decodeBase64String() }.getOrNull()?.let { add(it) }
        }
        for (text in candidates) {
            if (!text.trimStart().startsWith("{")) continue
            runCatching {
                val obj = json.parseToJsonElement(text).jsonObject
                fun str(vararg keys: String): String {
                    for (k in keys) {
                        val el = obj[k]
                        if (el is JsonPrimitive && el.content.isNotBlank()) return el.content
                    }
                    return ""
                }
                EndpointImport(
                    url = str("url", "base_url", "server_url"),
                    primaryUrl = str("primary_url", "primaryUrl"),
                    localUrl = str("local_url", "localUrl"),
                    token = str("token", "deviceToken"),
                    deviceToken = str("device_token", "device_token"),
                    name = str("name"),
                )
            }.getOrNull()?.let { imp ->
                if (imp.primary().isNotBlank()) return imp
            }
        }
        return null
    }

    /** 生成可分享的配置文本（base64(JSON)），用于「导出配置」。 */
    fun exportConfig(): String {
        val imp = EndpointImport(
            primaryUrl = config.primaryUrl,
            localUrl = config.localUrl,
            deviceToken = config.deviceToken,
        )
        return json.encodeToString(EndpointImport.serializer(), imp).encodeBase64()
    }

    /** 解析 LAN 配对二维码载荷。 */
    fun parseLanPairing(raw: String): LanPairingPayload? {
        val trimmed = raw.trim()
        val candidates = buildList {
            add(trimmed)
            runCatching { trimmed.decodeBase64String() }.getOrNull()?.let { add(it) }
        }
        for (text in candidates) {
            if (!text.trimStart().startsWith("{")) continue
            runCatching {
                json.decodeFromString(LanPairingPayload.serializer(), text)
            }.getOrNull()?.let { p ->
                if (p.endpoint.isNotBlank() && p.code.isNotBlank()) return p
            }
        }
        return null
    }

    private fun persist() {
        store?.save(config)
    }
}
