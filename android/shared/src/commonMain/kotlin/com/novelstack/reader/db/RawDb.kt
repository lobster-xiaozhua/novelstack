package com.novelstack.reader.db

import app.cash.sqldelight.db.QueryResult
import app.cash.sqldelight.db.SqlCursor
import app.cash.sqldelight.db.SqlDriver
import app.cash.sqldelight.db.SqlPreparedStatement

/**
 * 直接执行 SQL 的小封装。
 *
 * 为什么需要它：FTS5 虚拟表是运行时能力（SQLCipher 编译选项不同），
 * 不能写进 SQLDelight 的静态 schema；FTS 的建表、写入、MATCH 查询都走这里。
 * 普通表仍由 SQLDelight 生成类型安全代码。
 */
internal class RawDb(private val driver: SqlDriver) {

    fun exec(sql: String, args: List<Any?> = emptyList()) {
        driver.execute(null, sql, args.size) { bind(args) }
    }

    fun <T> query(sql: String, args: List<Any?> = emptyList(), map: (SqlCursor) -> T): List<T> {
        return driver.executeQuery(
            identifier = null,
            sql = sql,
            mapper = { cursor ->
                val out = ArrayList<T>()
                while (cursor.next().value) out.add(map(cursor))
                QueryResult.Value(out)
            },
            parameters = args.size,
            binders = { bind(args) },
        ).value
    }

    fun long(sql: String, args: List<Any?> = emptyList()): Long? =
        query(sql, args) { it.getLong(0) }.firstOrNull()
}

private fun SqlPreparedStatement.bind(args: List<Any?>) {
    args.forEachIndexed { i, v ->
        // SqlDelight 的 SqlPreparedStatement 约定索引从 0 开始：
        // Android 驱动内部会 +1 转成 SupportSQLiteStatement 的 1-based，
        // JDBC 驱动则直接透传给 sqlite-jdbc 的 0-based API。
        // 这里若写成 i+1，两端都会错位（JVM 上直接数组越界）。
        val idx = i
        when (v) {
            // SQLDelight 2.0 的 SqlPreparedStatement 没有 bindNull：绑一个 null 字符串即为 NULL。
            null -> bindString(idx, null)
            is String -> bindString(idx, v)
            is Int -> bindLong(idx, v.toLong())
            is Long -> bindLong(idx, v)
            is Double -> bindDouble(idx, v)
            is Float -> bindDouble(idx, v.toDouble())
            is Boolean -> bindLong(idx, if (v) 1L else 0L)
            is ByteArray -> bindBytes(idx, v)
            else -> bindString(idx, v.toString())
        }
    }
}
