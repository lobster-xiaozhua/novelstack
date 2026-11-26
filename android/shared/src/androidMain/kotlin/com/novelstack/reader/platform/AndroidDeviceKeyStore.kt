package com.novelstack.reader.platform

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.io.File
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.SecureRandom
import java.security.spec.MGF1ParameterSpec
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.spec.OAEPParameterSpec
import javax.crypto.spec.PSource

/**
 * Android Keystore 实现。
 *
 * 两个条目：
 *  - `novelstack-device-rsa-v1`：RSA-2048，OAEP(SHA-256) 解密服务端包装的书籍/音频 DEK；
 *  - `novelstack-local-aes-v1`：AES-256-GCM，包装 SQLCipher 库密钥（32B 随机）。
 *
 * 私钥与 AES 密钥都在 Keystore 内，`getKey` 返回的是不可导出的句柄；
 * 本类没有任何“导出密钥”的方法。
 */
class AndroidDeviceKeyStore(private val context: Context) : DeviceKeyStore {

    private val ks: KeyStore = KeyStore.getInstance(PROVIDER).apply { load(null) }

    override fun ensureKeys() {
        if (!ks.containsAlias(RSA_ALIAS)) generateRsa()
        if (!ks.containsAlias(AES_ALIAS)) generateAes()
    }

    private fun generateRsa() {
        val spec = KeyGenParameterSpec.Builder(
            RSA_ALIAS,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
        )
            .setKeySize(2048)
            .setDigests(KeyProperties.DIGEST_SHA256, KeyProperties.DIGEST_SHA1)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_RSA_OAEP)
            .build()
        val kpg = KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_RSA, PROVIDER)
        kpg.initialize(spec)
        kpg.generateKeyPair()
    }

    private fun generateAes() {
        val spec = KeyGenParameterSpec.Builder(
            AES_ALIAS,
            KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
        )
            .setKeySize(256)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
            .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
            .setRandomizedEncryptionRequired(true)
            .build()
        val kg = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, PROVIDER)
        kg.init(spec)
        kg.generateKey()
    }

    override fun publicKeyPem(): String {
        ensureKeys()
        val cert = ks.getCertificate(RSA_ALIAS)
            ?: error("device RSA key missing")
        val der = cert.publicKey.encoded
        val b64 = Base64.encodeToString(der, Base64.NO_WRAP)
        val sb = StringBuilder()
        sb.append("-----BEGIN PUBLIC KEY-----\n")
        var i = 0
        while (i < b64.length) {
            val end = minOf(i + 64, b64.length)
            sb.append(b64, i, end).append('\n')
            i = end
        }
        sb.append("-----END PUBLIC KEY-----\n")
        return sb.toString()
    }

    override fun unwrapDEK(wrappedB64: String): ByteArray {
        ensureKeys()
        val key = ks.getKey(RSA_ALIAS, null) ?: error("device RSA key missing")
        val cipher = Cipher.getInstance("RSA/ECB/OAEPWithSHA-256AndMGF1Padding")
        // Android 的默认 MGF1 常用 SHA-1，必须显式指定 SHA-256，才能与 Go 的
        // rsa.EncryptOAEP(sha256.New(), ...) 对齐。
        cipher.init(
            Cipher.DECRYPT_MODE,
            key,
            OAEPParameterSpec(
                "SHA-256", "MGF1", MGF1ParameterSpec.SHA256, PSource.PSpecified.DEFAULT,
            ),
        )
        return cipher.doFinal(Base64.decode(wrappedB64, Base64.NO_WRAP))
    }

    override fun dbPassphrase(): ByteArray {
        ensureKeys()
        val f = File(context.filesDir, KEY_FILE)
        if (!f.exists()) {
            val pass = ByteArray(32).also { SecureRandom().nextBytes(it) }
            f.writeBytes(sealLocal(pass))
            return pass
        }
        return openLocal(f.readBytes())
    }

    private fun sealLocal(plain: ByteArray): ByteArray {
        val key = ks.getKey(AES_ALIAS, null) ?: error("local AES key missing")
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.ENCRYPT_MODE, key)
        val ct = cipher.doFinal(plain)
        val iv = cipher.iv
        // 格式：1 字节 IV 长度 + IV + 密文（GCM tag 已含在密文尾部）。
        return byteArrayOf(iv.size.toByte()) + iv + ct
    }

    private fun openLocal(blob: ByteArray): ByteArray {
        require(blob.size > 2) { "corrupt key file" }
        val ivLen = blob[0].toInt() and 0xff
        require(ivLen in 12..16 && blob.size > 1 + ivLen) { "corrupt key file" }
        val iv = blob.copyOfRange(1, 1 + ivLen)
        val ct = blob.copyOfRange(1 + ivLen, blob.size)
        val key = ks.getKey(AES_ALIAS, null) ?: error("local AES key missing")
        val cipher = Cipher.getInstance("AES/GCM/NoPadding")
        cipher.init(Cipher.DECRYPT_MODE, key, javax.crypto.spec.GCMParameterSpec(128, iv))
        return cipher.doFinal(ct)
    }

    override fun wipe() {
        File(context.filesDir, KEY_FILE).delete()
        runCatching { ks.deleteEntry(RSA_ALIAS) }
        runCatching { ks.deleteEntry(AES_ALIAS) }
    }

    companion object {
        private const val PROVIDER = "AndroidKeyStore"
        private const val RSA_ALIAS = "novelstack-device-rsa-v1"
        private const val AES_ALIAS = "novelstack-local-aes-v1"
        private const val KEY_FILE = "reader.key"
    }
}

class AndroidDataWiper(private val context: Context) : LocalDataWiper {
    override fun wipeAll() {
        val db = context.getDatabasePath("reader.db")
        listOf(db, File(db.path + "-wal"), File(db.path + "-shm"), File(db.path + "-journal"))
            .forEach { runCatching { it.delete() } }
        // 本地索引/音频临时文件（TTS 只在内存解密，这里主要是防御性清理）。
        context.cacheDir?.listFiles()?.forEach { runCatching { it.deleteRecursively() } }
    }
}
