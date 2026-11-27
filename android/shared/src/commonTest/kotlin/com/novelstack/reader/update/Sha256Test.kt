package com.novelstack.reader.update

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class Sha256Test {

    @Test
    fun emptyInputMatchesKnownVector() {
        assertEquals(
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
            Sha256.hex(ByteArray(0)),
        )
    }

    @Test
    fun abcMatchesKnownVector() {
        assertEquals(
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
            Sha256.hex("abc".encodeToByteArray()),
        )
    }

    @Test
    fun helloMatchesKnownVector() {
        assertEquals(
            "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824",
            Sha256.hex("hello".encodeToByteArray()),
        )
    }

    @Test
    fun multiBlockInputIsStable() {
        // 长度超过 64 字节，覆盖多块与填充边界。
        val data = ByteArray(200) { (it % 251).toByte() }
        val a = Sha256.hex(data)
        val b = Sha256.hex(data.copyOf())
        assertEquals(a, b)
        assertEquals(64, a.length)
    }

    @Test
    fun matchesIsCaseInsensitiveAndRejectsEmpty() {
        val data = "novelstack".encodeToByteArray()
        val hex = Sha256.hex(data)
        assertTrue(Sha256.matches(data, hex))
        assertTrue(Sha256.matches(data, hex.uppercase()))
        assertFalse(Sha256.matches(data, ""))
        assertFalse(Sha256.matches(data, "deadbeef"))
    }
}
