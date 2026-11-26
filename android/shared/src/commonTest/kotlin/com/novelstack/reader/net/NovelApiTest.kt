package com.novelstack.reader.net

import kotlin.test.Test
import kotlin.test.assertEquals

/**
 * 服务端地址规范化：用户只输入主机名时，局域网/模拟器按 http、公网按 https 补全。
 * 写错会让 APP 一直连不上（或明文发到公网），因此单独覆盖。
 */
class NovelApiTest {

    @Test
    fun emptyFallsBackToLocalhost() {
        assertEquals("https://localhost", NovelApi.normalizeServer(""))
        assertEquals("https://localhost", NovelApi.normalizeServer("   "))
    }

    @Test
    fun keepsExplicitSchemeAndTrimsSlash() {
        assertEquals("https://reader.example.com", NovelApi.normalizeServer("https://reader.example.com/"))
        assertEquals("http://10.0.2.2:8080", NovelApi.normalizeServer("http://10.0.2.2:8080//"))
        assertEquals("https://reader.example.com:8443", NovelApi.normalizeServer("  https://reader.example.com:8443  "))
    }

    @Test
    fun emulatorHostUsesCleartext() {
        // 10.0.2.2 是 Android 模拟器访问宿主机的固定地址
        assertEquals("http://10.0.2.2:8080", NovelApi.normalizeServer("10.0.2.2:8080"))
        assertEquals("http://localhost:8080", NovelApi.normalizeServer("localhost:8080"))
        assertEquals("http://127.0.0.1:8443", NovelApi.normalizeServer("127.0.0.1:8443/"))
    }

    @Test
    fun privateRangesUseCleartext() {
        assertEquals("http://192.168.1.10:8080", NovelApi.normalizeServer("192.168.1.10:8080"))
        assertEquals("http://10.1.2.3", NovelApi.normalizeServer("10.1.2.3"))
        assertEquals("http://172.16.0.9:8080", NovelApi.normalizeServer("172.16.0.9:8080"))
        assertEquals("http://172.31.255.1", NovelApi.normalizeServer("172.31.255.1"))
    }

    @Test
    fun publicHostsUseTls() {
        assertEquals("https://reader.example.com", NovelApi.normalizeServer("reader.example.com"))
        assertEquals("https://172.32.0.1", NovelApi.normalizeServer("172.32.0.1"))
        assertEquals("https://8.8.8.8:8443", NovelApi.normalizeServer("8.8.8.8:8443"))
    }
}
