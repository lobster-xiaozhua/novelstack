package com.novelstack.reader.net

import android.content.Context
import android.content.SharedPreferences
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json

/**
 * Android 的端点配置存储。
 *
 * 用 SharedPreferences（非加密）是刻意的：这里只放服务器地址与设备 token，
 * 真正的书籍密钥/DEK 都在 Android Keystore 与 SQLCipher 内。
 * token 若泄露只能读密文书库（服务端只下发被设备公钥包装的 DEK），且可随时撤销。
 */
class AndroidEndpointStore(context: Context, private val json: Json) : EndpointStore {
    private val prefs: SharedPreferences =
        context.getSharedPreferences("novelstack_endpoints", Context.MODE_PRIVATE)

    override fun load(): EndpointConfig {
        val raw = prefs.getString(KEY_CONFIG, null) ?: return EndpointConfig()
        return runCatching { json.decodeFromString(EndpointConfig.serializer(), raw) }
            .getOrDefault(EndpointConfig())
    }

    override fun save(config: EndpointConfig) {
        prefs.edit().putString(KEY_CONFIG, json.encodeToString(EndpointConfig.serializer(), config)).apply()
    }

    override fun clear() {
        prefs.edit().clear().apply()
    }

    private companion object {
        const val KEY_CONFIG = "endpoint_config"
    }
}
