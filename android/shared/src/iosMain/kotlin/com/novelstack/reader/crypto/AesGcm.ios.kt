package com.novelstack.reader.crypto

/**
 * iOS 预留实现：本阶段不验收 iOS，但保留 target 与接口。
 * 接入时用 CryptoKit 的 AES.GCM.open(_:using:authenticating:) 实现。
 */
actual object AesGcm {
    actual fun open(
        key: ByteArray,
        nonce: ByteArray,
        ciphertext: ByteArray,
        aad: ByteArray,
    ): ByteArray = error("iOS AES-GCM 尚未实现（第五阶段不验收 iOS）")

    actual fun seal(
        key: ByteArray,
        nonce: ByteArray,
        plaintext: ByteArray,
        aad: ByteArray,
    ): ByteArray = error("iOS AES-GCM 尚未实现（第五阶段不验收 iOS）")
}
