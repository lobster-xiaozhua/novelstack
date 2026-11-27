package com.novelstack.reader.telemetry

import android.content.Context
import app.cash.sqldelight.db.SqlDriver
import app.cash.sqldelight.driver.android.AndroidSqliteDriver
import com.novelstack.reader.telemetry.db.TelemetryDatabase
import net.zetetic.database.sqlcipher.SupportOpenHelperFactory

/** 数据库句柄：SQLDelight 的 Database 本身不暴露 close，需要保留 driver。 */
class TelemetryHandle(
    val database: TelemetryDatabase,
    private val driver: SqlDriver,
) : AutoCloseable {
    override fun close() {
        runCatching { driver.close() }
    }
}

/**
 * 打开遥测专用库 telemetry.db（SQLCipher 整库加密）。
 *
 * 与 reader.db 物理隔离：崩溃写入不会被业务库的长事务或锁竞争挡住，
 * 「清除遥测」也可以整库删除而不触碰书籍与进度。
 */
fun openTelemetryDatabase(context: Context, passphrase: ByteArray): TelemetryHandle {
    System.loadLibrary("sqlcipher")
    val driver = AndroidSqliteDriver(
        schema = TelemetryDatabase.Schema,
        context = context,
        name = "telemetry.db",
        factory = SupportOpenHelperFactory(passphrase),
    )
    return TelemetryHandle(TelemetryDatabase(driver), driver)
}
