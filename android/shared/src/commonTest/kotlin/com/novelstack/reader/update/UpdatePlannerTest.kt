package com.novelstack.reader.update

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

class UpdatePlannerTest {

    @Test
    fun picksLowestLatencyMirror() {
        val probes = listOf<Pair<String, Long>>(
            "https://a/apk" to 320,
            "https://b/apk" to 95,
            "https://c/apk" to 210,
        )
        assertEquals("https://b/apk", UpdatePlanner.pickFastest(probes))
    }

    @Test
    fun ignoresFailedProbes() {
        val probes = listOf<Pair<String, Long>>(
            "https://a/apk" to 0,
            "https://b/apk" to 130,
        )
        assertEquals("https://b/apk", UpdatePlanner.pickFastest(probes))
    }

    @Test
    fun returnsNullWhenAllFail() {
        val probes = listOf<Pair<String, Long>>(
            "https://a/apk" to 0,
            "https://b/apk" to -1,
        )
        assertNull(UpdatePlanner.pickFastest(probes))
        assertNull(UpdatePlanner.pickFastest(emptyList()))
    }

    @Test
    fun tieKeepsFirstSeen() {
        val probes = listOf<Pair<String, Long>>(
            "https://a/apk" to 100,
            "https://b/apk" to 100,
        )
        assertEquals("https://a/apk", UpdatePlanner.pickFastest(probes))
    }

    @Test
    fun verifyUsesRealHash() {
        val bytes = "payload".encodeToByteArray()
        val hex = Sha256.hex(bytes)
        assertTrue(UpdatePlanner.verify(bytes, hex))
        assertFalse(UpdatePlanner.verify(bytes, Sha256.hex("other".encodeToByteArray())))
    }

    @Test
    fun versionComparison() {
        assertTrue(UpdatePlanner.isNewer("1.0.0", "0.9.9"))
        assertTrue(UpdatePlanner.isNewer("1.0.10", "1.0.9"))
        assertTrue(UpdatePlanner.isNewer("v1.2.0", "1.1.9"))
        assertFalse(UpdatePlanner.isNewer("1.0.0", "1.0.0"))
        assertFalse(UpdatePlanner.isNewer("0.9.0", "1.0.0"))
        assertFalse(UpdatePlanner.isNewer("", "1.0.0"))
    }
}
