package com.novelstack.reader.crypto

/**
 * AES-256-GCM。放 expect/actual 是因为 common 没有 JCA；
 * Android 用 javax.crypto，iOS 预留 CryptoKit 实现（本阶段不验收）。
 */
expect object AesGcm {
    fun open(key: ByteArray, nonce: ByteArray, ciphertext: ByteArray, aad: ByteArray): ByteArray
    fun seal(key: ByteArray, nonce: ByteArray, plaintext: ByteArray, aad: ByteArray): ByteArray
}

/** 尽力清零敏感内存（Kotlin 不保证 JIT 不复制，但保持习惯）。 */
fun zeroBytes(b: ByteArray?) {
    if (b == null) return
    for (i in b.indices) b[i] = 0
}
