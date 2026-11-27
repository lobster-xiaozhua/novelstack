package com.novelstack.reader.net

import kotlin.test.AfterTest
import kotlin.test.BeforeTest
import kotlin.test.Test
import kotlin.test.assertFalse
import kotlin.test.assertTrue

/** 类名带 Timeout 的异常会被 isTimeout 按类名兜底识别（模拟各平台底层超时）。 */
private class FakeSocketTimeoutException : Exception("timed out")

/** 普通异常：不参与熔断计数。 */
private class FakeBoomException : Exception("boom")

/**
 * 离线熔断：连续 3 次超时才进入离线，任意一次成功立即恢复。
 * 4xx/5xx 与业务异常不计入——服务器是活的，不该切离线。
 */
class OfflineCircuitTest {

    @BeforeTest
    fun setUp() = NetworkCircuit.clear()

    @AfterTest
    fun tearDown() = NetworkCircuit.clear()

    @Test
    fun threeConsecutiveTimeoutsTripTheBreaker() {
        NetworkCircuit.onFailure(FakeSocketTimeoutException())
        NetworkCircuit.onFailure(FakeSocketTimeoutException())
        assertFalse(NetworkCircuit.offline.value, "两次超时不应进入离线模式")

        NetworkCircuit.onFailure(FakeSocketTimeoutException())
        assertTrue(NetworkCircuit.offline.value, "第三次超时应进入离线模式")
    }

    @Test
    fun successResetsCounter() {
        NetworkCircuit.onFailure(FakeSocketTimeoutException())
        NetworkCircuit.onFailure(FakeSocketTimeoutException())
        NetworkCircuit.onSuccess()
        NetworkCircuit.onFailure(FakeSocketTimeoutException())
        NetworkCircuit.onFailure(FakeSocketTimeoutException())
        assertFalse(NetworkCircuit.offline.value, "成功一次后计数应归零")
    }

    @Test
    fun successClearsOffline() {
        repeat(NetworkCircuit.THRESHOLD) { NetworkCircuit.onFailure(FakeSocketTimeoutException()) }
        assertTrue(NetworkCircuit.offline.value)
        NetworkCircuit.onSuccess()
        assertFalse(NetworkCircuit.offline.value)
    }

    @Test
    fun nonTimeoutErrorsDoNotCount() {
        repeat(10) { NetworkCircuit.onFailure(FakeBoomException()) }
        assertFalse(NetworkCircuit.offline.value, "非超时错误不应触发离线模式")
    }

    @Test
    fun isTimeoutWalksCauseChain() {
        val wrapped = RuntimeException("wrapper", FakeSocketTimeoutException())
        assertTrue(isTimeout(wrapped))
        assertFalse(isTimeout(FakeBoomException()))
    }
}
