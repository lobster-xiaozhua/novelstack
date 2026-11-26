package com.novelstack.reader.platform

/**
 * 设备密钥存储。实现必须满足：
 *  - RSA 私钥只存在于 Android Keystore / iOS Keychain，永不导出；
 *  - SQLCipher 库密钥是随机 32 字节，由 Keystore 内的 AES 密钥包装后落盘；
 *  - 任何密钥都不得写入 SharedPreferences、DataStore 明文或日志。
 */
interface DeviceKeyStore {
    fun ensureKeys()

    /** 设备公钥（SPKI PEM），注册设备时上传给服务端。 */
    fun publicKeyPem(): String

    /** 用 Keystore 私钥解开服务端用设备公钥包装的 DEK（32 字节）。 */
    fun unwrapDEK(wrappedB64: String): ByteArray

    /** 返回 SQLCipher 库密钥；首次调用时生成并用 Keystore AES 包装落盘。 */
    fun dbPassphrase(): ByteArray

    /** 删除 Keystore 条目与本地密钥文件（先删数据库再调用）。 */
    fun wipe()
}

/** 清除本地数据的平台实现：删除 SQLCipher 数据库及其 WAL/SHM。 */
interface LocalDataWiper {
    fun wipeAll()
}

/** 顺序播放 PCM 的音频输出。 */
interface AudioSink {
    fun start(sampleRate: Int, channels: Int)
    fun write(pcm: ByteArray)
    fun pause()
    fun resume()
    fun stop()
    fun setSpeed(speed: Float)
}

interface AudioSinkFactory {
    fun create(): AudioSink
}
