package com.novelstack.reader.telemetry

import android.content.Context
import com.novelstack.reader.data.createReaderStore
import com.novelstack.reader.net.NovelApi
import com.novelstack.reader.net.createHttpClient
import com.novelstack.reader.platform.AndroidDeviceKeyStore
import kotlinx.serialization.json.Json

/**
 * Worker / 前台服务里的遥测装配。
 *
 * 与 Application 的区别：这里没有内存里的 AppState，必须自己从 SQLCipher
 * 业务库读出 server_url / device_token / device_id，再装配 API 与遥测库。
 */
object AndroidTelemetryRuntime {

    private val json = Json { ignoreUnknownKeys = true }

    class Session(
        val api: NovelApi,
        val repo: TelemetryRepository,
        val deviceId: String,
        private val handle: TelemetryHandle,
    ) : AutoCloseable {
        override fun close() {
            handle.close()
        }
    }

    /** 未配对（没有 server_url / token）时返回 null。 */
    fun open(context: Context): Session? {
        val keyStore = AndroidDeviceKeyStore(context)
        val reader = createReaderStore(context, keyStore)
        val server = reader.kvGet("server_url")
        val token = reader.kvGet("device_token")
        if (server.isNullOrBlank() || token.isNullOrBlank()) return null
        val deviceId = reader.kvGet("device_id") ?: ""
        val api = NovelApi(createHttpClient(), json).apply { configure(server, token) }
        val handle = openTelemetryDatabase(context, keyStore.dbPassphrase())
        return Session(api, TelemetryRepository(handle.database), deviceId, handle)
    }
}
