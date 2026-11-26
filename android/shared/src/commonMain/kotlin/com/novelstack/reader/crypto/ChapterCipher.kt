package com.novelstack.reader.crypto

/**
 * 服务端「单章密文包」（NSCH）的客户端实现。
 *
 * 格式（全部小端）：
 *   0  magic "NSCH"
 *   4  version 1
 *   5  reserved
 *   6  flags u16
 *   8  chapterIndex u32
 *   12 plainLen u32
 *   16 nonce 12B
 *   28 ciphertext + tag
 *
 * AAD = "novelstack-mobile-chapter-v1|" + bookID + "|" + header[0:16]
 * 与 internal/chapterpkg 完全一致；任何头部字段被改动都会导致解密失败。
 */
object ChapterCipher {
    private const val HEADER = 28
    private const val AAD_PREFIX = "novelstack-mobile-chapter-v1|"

    fun open(dek: ByteArray, bookId: String, index: Int, pkg: ByteArray): ByteArray {
        require(pkg.size >= HEADER + 16) { "章节包过短" }
        require(String(pkg, 0, 4, Charsets.US_ASCII) == "NSCH") { "章节包标识错误" }
        require(pkg[4].toInt() == 1) { "不支持的章节包版本" }
        val idx = le32(pkg, 8)
        require(idx == index) { "章节索引不匹配" }
        val n = le32(pkg, 12)
        require(n >= 0 && n <= 64 * 1024 * 1024) { "章节长度异常" }
        require(pkg.size == HEADER + n + 16) { "章节包长度不匹配" }
        val aad = buildAad(bookId, pkg)
        val nonce = pkg.copyOfRange(16, 28)
        val ct = pkg.copyOfRange(HEADER, pkg.size)
        return AesGcm.open(dek, nonce, ct, aad)
    }

    private fun buildAad(bookId: String, pkg: ByteArray): ByteArray {
        val prefix = (AAD_PREFIX + bookId + "|").encodeToByteArray()
        val out = ByteArray(prefix.size + 16)
        prefix.copyInto(out, 0)
        pkg.copyInto(out, prefix.size, 0, 16)
        return out
    }

    private fun le32(b: ByteArray, off: Int): Int =
        (b[off].toInt() and 0xff) or
            ((b[off + 1].toInt() and 0xff) shl 8) or
            ((b[off + 2].toInt() and 0xff) shl 16) or
            ((b[off + 3].toInt() and 0xff) shl 24)
}
