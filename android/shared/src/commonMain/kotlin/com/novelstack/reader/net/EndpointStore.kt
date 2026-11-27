package com.novelstack.reader.net

/**
 * 服务端地址与凭证的本地持久化抽象。
 *
 * commonMain 只定义契约，Android 用 SharedPreferences 实现，iOS 可用 NSUserDefaults。
 * 之所以不复用 SQLCipher 书库：地址配置需要在「未配对 / 数据库被清除」时仍可读写，
 * 且它本身不是敏感正文（token 另存于 Keystore，见 DeviceKeyStore）。
 */
interface EndpointStore {
    fun load(): EndpointConfig
    fun save(config: EndpointConfig)
    fun clear()
}
