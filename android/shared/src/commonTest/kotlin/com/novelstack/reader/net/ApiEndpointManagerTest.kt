package com.novelstack.reader.net

import io.ktor.util.encodeBase64
import kotlinx.serialization.json.Json
import kotlin.test.BeforeTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

/**
 * 纯 JVM 单测：验证动态根地址的核心逻辑，不依赖 Android。
 * 用假 EndpointStore 避免 SharedPreferences。
 */
class ApiEndpointManagerTest {

    private class FakeStore : EndpointStore {
        var current: EndpointConfig = EndpointConfig()
        var cleared = false
        override fun load(): EndpointConfig = current
        override fun save(config: EndpointConfig) { current = config }
        override fun clear() { cleared = true; current = EndpointConfig() }
    }

    private lateinit var store: FakeStore

    @BeforeTest
    fun setUp() {
        store = FakeStore()
        ApiEndpointManager.install(store, Json { ignoreUnknownKeys = true })
    }

    @Test
    fun `no local url uses primary`() {
        ApiEndpointManager.configure("https://api.example.com", "", "tok")
        assertEquals("https://api.example.com", ApiEndpointManager.activeBaseUrl())
        assertFalse(ApiEndpointManager.isLocalActive())
    }

    @Test
    fun `local falls back to primary when unreachable`() {
        ApiEndpointManager.configure("https://api.example.com", "http://192.168.1.5:8080", "tok")
        // 未探测 → 默认公网
        assertEquals("https://api.example.com", ApiEndpointManager.activeBaseUrl())
        // 探测成功 → 切内网
        ApiEndpointManager.updateLocalProbe(true)
        assertEquals("http://192.168.1.5:8080", ApiEndpointManager.activeBaseUrl())
        assertTrue(ApiEndpointManager.isLocalActive())
        // 探测失败 → 回落公网
        ApiEndpointManager.updateLocalProbe(false)
        assertEquals("https://api.example.com", ApiEndpointManager.activeBaseUrl())
    }

    @Test
    fun `probe result is cached within ttl`() {
        ApiEndpointManager.configure("https://api.example.com", "http://192.168.1.5:8080", "tok")
        ApiEndpointManager.updateLocalProbe(true)
        val probedAt = ApiEndpointManager.snapshot().localProbedAt
        // TTL 内相同结果重复上报不应刷新时间戳（去重，避免抖动）。
        ApiEndpointManager.updateLocalProbe(true)
        assertEquals(probedAt, ApiEndpointManager.snapshot().localProbedAt)
        assertEquals("http://192.168.1.5:8080", ApiEndpointManager.activeBaseUrl())
        // 探测结果变化则立即更新（例如从内网切回公网）。
        ApiEndpointManager.updateLocalProbe(false)
        assertEquals("https://api.example.com", ApiEndpointManager.activeBaseUrl())
    }

    @Test
    fun `parse import from json`() {
        val raw = """{"url":"https://api.example.com","local_url":"http://192.168.1.5:8080","token":"nsd_abc"}"""
        val imp = ApiEndpointManager.parseImport(raw)
        assertEquals("https://api.example.com", imp?.primary())
        assertEquals("http://192.168.1.5:8080", imp?.localUrl)
        assertEquals("nsd_abc", imp?.resolvedToken())
    }

    @Test
    fun `parse import from base64 json`() {
        val raw = """{"primary_url":"https://p.example.com","device_token":"nsd_x"}"""
        val encoded = raw.encodeBase64()
        val imp = ApiEndpointManager.parseImport(encoded)
        assertEquals("https://p.example.com", imp?.primary())
        assertEquals("nsd_x", imp?.resolvedToken())
    }

    @Test
    fun `parse import rejects garbage`() {
        assertNull(ApiEndpointManager.parseImport("not a config"))
        assertNull(ApiEndpointManager.parseImport(""))
    }

    @Test
    fun `export then import round trip`() {
        ApiEndpointManager.configure("https://api.example.com", "http://192.168.1.5:8080", "nsd_token")
        val exported = ApiEndpointManager.exportConfig()
        // 先清空
        ApiEndpointManager.clear()
        assertFalse(ApiEndpointManager.hasConfig())
        val imp = ApiEndpointManager.parseImport(exported)
        assertEquals("https://api.example.com", imp?.primary())
        assertEquals("nsd_token", imp?.resolvedToken())
    }

    @Test
    fun `parse lan pairing payload`() {
        val raw = """{"v":1,"endpoint":"http://192.168.1.5:8080","code":"123456"}"""
        val p = ApiEndpointManager.parseLanPairing(raw)
        assertEquals("123456", p?.code)
        assertEquals("http://192.168.1.5:8080", p?.endpoint)
    }

    @Test
    fun `normalize server adds scheme correctly`() {
        assertEquals("http://192.168.1.100:8443", NovelApi.normalizeServer("192.168.1.100:8443"))
        assertEquals("https://api.example.com", NovelApi.normalizeServer("api.example.com"))
        assertEquals("http://10.0.2.2:8080", NovelApi.normalizeServer("10.0.2.2:8080"))
        assertEquals("https://example.com", NovelApi.normalizeServer("https://example.com/"))
    }
}
