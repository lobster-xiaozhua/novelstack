package com.novelstack.reader.data

import app.cash.sqldelight.driver.android.AndroidSqliteDriver
import com.novelstack.reader.db.ReaderDatabase
import com.novelstack.reader.db.ReaderRepository
import com.novelstack.reader.platform.DeviceKeyStore
import net.zetetic.database.sqlcipher.SupportOpenHelperFactory

/**
 * 打开 SQLCipher 加密库。
 *
 * 顺序很重要：
 *  1. 从 Keystore 取（或首次生成）32 字节库密钥；
 *  2. SQLCipher 用该密钥打开/创建 reader.db；
 *  3. 用 raw SQL 建立 FTS5 虚拟表（能力检测在 ReaderRepository.initFts 内）。
 *
 * 数据库文件、WAL、SHM 都是密文；密钥明文只在这一小段时间存在于内存。
 */
fun createReaderStore(
    context: android.content.Context,
    keyStore: DeviceKeyStore,
): ReaderRepository {
    System.loadLibrary("sqlcipher")
    val passphrase = keyStore.dbPassphrase()
    val factory = SupportOpenHelperFactory(passphrase)
    val driver = AndroidSqliteDriver(
        schema = ReaderDatabase.Schema,
        context = context,
        name = "reader.db",
        factory = factory,
    )
    val repo = ReaderRepository(ReaderDatabase(driver), driver)
    repo.initFts()
    return repo
}
