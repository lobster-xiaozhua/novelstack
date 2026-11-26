package com.novelstack.reader.crypto

import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertTrue

/**
 * 章节密文包（NSCH）的客户端解密测试。
 *
 * 关键的一条是 opensServerFixture：密文由 Go 侧 internal/chapterpkg.Seal 生成并硬编码，
 * 用来保证两端 AAD/头部布局逐字节一致——如果哪一端改了 AAD 构造，这里立刻失败。
 */
class ChapterCipherTest {

    private val dek = ByteArray(32) { i -> i.toByte() }
    private val bookId = "bk_01H8ZK"
    private val index = 3
    private val plainText = "第三章 归途\n天亮的时候，雨停了。他沿着河堤往回走。"

    // go run ./tmpgen 的输出（DEK=00..1f, book=bk_01H8ZK, index=3）
    private val serverFixtureHex =
        "4e53434801000100030000004a0000006a18a256410b57c900df4b97c46be9ff13194e56d36b4c098fdd23b0" +
            "2a51502fbcb052db4607be31fcb6c9c92d270929cb09541019a8cdd207f7907b9fc3fdf70bae4f24bb61db83" +
            "531fc01c272d8bc4aeb16042ca919aad1b06c9df3b2eca8a5999772d5345"

    @Test
    fun opensServerFixture() {
        val pkg = hex(serverFixtureHex)
        assertEquals(28 + plainText.encodeToByteArray().size + 16, pkg.size)
        val plain = ChapterCipher.open(dek, bookId, index, pkg)
        assertEquals(plainText, plain.decodeToString())
    }

    @Test
    fun roundTripWithLocalSeal() {
        val plain = "第一章\n夜航船靠岸。".encodeToByteArray()
        val pkg = sealForTest(plain, index = 0)
        assertContentEquals(plain, ChapterCipher.open(dek, bookId, 0, pkg))
    }

    @Test
    fun rejectsEveryTamperedHeaderByte() {
        val base = hex(serverFixtureHex)
        // 头部前 16 字节全部参与 AAD（含 magic/version/flags/index/plainLen）
        for (i in 0 until 16) {
            val bad = base.copyOf()
            bad[i] = (bad[i].toInt() xor 0x01).toByte()
            assertFailsWith<Exception>("第 $i 字节被篡改仍解密成功", block = {
                ChapterCipher.open(dek, bookId, index, bad)
            })
        }
        // 密文体任意翻转也必须失败（GCM 认证）
        for (i in listOf(28, 40, base.size - 1)) {
            val bad = base.copyOf()
            bad[i] = (bad[i].toInt() xor 0x80).toByte()
            assertFailsWith<Exception> { ChapterCipher.open(dek, bookId, index, bad) }
        }
    }

    @Test
    fun rejectsWrongContext() {
        val pkg = hex(serverFixtureHex)
        // 换书：AAD 里的 bookID 不同
        assertFailsWith<Exception> { ChapterCipher.open(dek, "bk_other", index, pkg) }
        // 换章：头部 index 与调用参数不一致（先被头部校验拦下）
        assertFailsWith<IllegalArgumentException> { ChapterCipher.open(dek, bookId, index + 1, pkg) }
        // 换 DEK
        val otherDek = ByteArray(32) { 0x5a }
        assertFailsWith<Exception> { ChapterCipher.open(otherDek, bookId, index, pkg) }
    }

    @Test
    fun rejectsMalformedPackage() {
        val pkg = hex(serverFixtureHex)
        assertFailsWith<IllegalArgumentException> { ChapterCipher.open(dek, bookId, index, pkg.copyOf(pkg.size - 1)) }
        assertFailsWith<IllegalArgumentException> { ChapterCipher.open(dek, bookId, index, ByteArray(10)) }

        val notNsch = pkg.copyOf()
        notNsch[0] = 'X'.code.toByte()
        assertFailsWith<IllegalArgumentException> { ChapterCipher.open(dek, bookId, index, notNsch) }

        val badVersion = pkg.copyOf()
        badVersion[4] = 2
        assertFailsWith<IllegalArgumentException> { ChapterCipher.open(dek, bookId, index, badVersion) }
    }

    @Test
    fun zeroBytesClearsBuffer() {
        val buf = byteArrayOf(1, 2, 3)
        zeroBytes(buf)
        assertTrue(buf.all { it == 0.toByte() })
    }

    // ---- 测试辅助：按服务端格式本地封包 ----

    private fun sealForTest(plain: ByteArray, index: Int): ByteArray {
        val header = ByteArray(28)
        "NSCH".encodeToByteArray().copyInto(header, 0)
        header[4] = 1
        header[5] = 0
        header[6] = 0
        header[7] = 0
        putLe32(header, 8, index)
        putLe32(header, 12, plain.size)
        val nonce = ByteArray(12) { i -> (0xA0 + i).toByte() }
        nonce.copyInto(header, 16)
        val aad = buildAad(bookId, header)
        val ct = AesGcm.seal(dek, nonce, plain, aad)
        return header + ct
    }

    private fun buildAad(book: String, header: ByteArray): ByteArray {
        val prefix = ("novelstack-mobile-chapter-v1|" + book + "|").encodeToByteArray()
        val out = ByteArray(prefix.size + 16)
        prefix.copyInto(out, 0)
        header.copyInto(out, prefix.size, 0, 16)
        return out
    }

    private fun putLe32(b: ByteArray, off: Int, v: Int) {
        b[off] = (v and 0xff).toByte()
        b[off + 1] = ((v ushr 8) and 0xff).toByte()
        b[off + 2] = ((v ushr 16) and 0xff).toByte()
        b[off + 3] = ((v ushr 24) and 0xff).toByte()
    }

    private fun hex(s: String): ByteArray {
        require(s.length % 2 == 0)
        return ByteArray(s.length / 2) { i ->
            ((s[i * 2].digitToInt(16) shl 4) or s[i * 2 + 1].digitToInt(16)).toByte()
        }
    }
}
