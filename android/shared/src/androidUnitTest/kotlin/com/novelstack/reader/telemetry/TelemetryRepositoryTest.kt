package com.novelstack.reader.telemetry

import app.cash.sqldelight.driver.jdbc.sqlite.JdbcSqliteDriver
import com.novelstack.reader.telemetry.db.TelemetryDatabase
import java.io.File
import java.util.Properties
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * 第九阶段核心验收：真实 SQLite 上的「合并、落盘、淘汰」。
 *
 * 用 JVM 版 SQLite 驱动而不是内存 fake，确保验证的是实际执行的 SQL。
 * 「进程被杀」用「关闭 driver 再重新打开同一个文件」模拟。
 */
class TelemetryRepositoryTest {

    private val meta = DeviceMeta("dev-1", "0.2.1", "Android 14", "Xiaomi 14")
    private val now = 1_700_000_000_000L

    private fun open(dbFile: File): Pair<JdbcSqliteDriver, TelemetryDatabase> {
        val isNew = !dbFile.exists()
        val driver = JdbcSqliteDriver("jdbc:sqlite:${dbFile.absolutePath}", Properties())
        if (isNew) TelemetryDatabase.Schema.create(driver)
        return driver to TelemetryDatabase(driver)
    }

    private fun newFile(): File =
        File.createTempFile("telemetry-test", ".db").also { it.delete() }

    private fun crash(name: String = "IllegalStateException", ts: Long = now): TelemetryEvent {
        val stack = "java.lang.IllegalStateException: boom\n\tat com.a.B.c(B.kt:12)"
        return TelemetrySanitizer.build(TelemetryKind.CRASH, name, ts, stack = stack)!!
    }

    @Test
    fun tenIdenticalCrashesCollapseToOneRowWithCountTen() {
        val file = newFile()
        val (driver, db) = open(file)
        val repo = TelemetryRepository(db)

        repeat(10) { repo.insert(crash(), meta) }

        val rows = repo.all()
        assertEquals(1, rows.size, "同一堆栈 5 分钟内应合并为 1 条")
        assertEquals(10, rows[0].count, "occurrence 应累加到 10")
        assertEquals(1, repo.stats().crashes)
        driver.close()
        file.delete()
    }

    @Test
    fun recordsSurviveProcessRestart() {
        val file = newFile()
        var (driver, db) = open(file)
        var repo = TelemetryRepository(db)
        repo.insert(crash("First"), meta)
        // 模拟进程被杀：不做任何 flush，直接关闭驱动。
        driver.close()

        val reopened = open(file)
        driver = reopened.first
        repo = TelemetryRepository(reopened.second)
        val rows = repo.all()
        assertEquals(1, rows.size, "重启后记录必须仍然可读")
        assertEquals("First", rows[0].name)
        driver.close()
        file.delete()
    }

    @Test
    fun mergeWindowDoesNotExtendForever() {
        val file = newFile()
        val (driver, db) = open(file)
        val repo = TelemetryRepository(db)
        val start = now
        repo.insert(crash(), meta)
        // 6 分钟后再来一次同样的崩溃：已超出 5 分钟合并窗口，应新建。
        repo.insert(crash(ts = start + 6 * 60 * 1000), meta)
        assertEquals(2, repo.all().size)
        driver.close()
        file.delete()
    }

    @Test
    fun capEvictsOldestButKeepsRecentCrashes() {
        val file = newFile()
        val (driver, db) = open(file)
        val repo = TelemetryRepository(db)

        // 用真实时钟：enforceCap 内部以 System.currentTimeMillis() 判定「24 小时内」，
        // 若这里用固定历史时间，崩溃会被误判为过期而淘汰（那正是本用例要防的回归）。
        val realNow = System.currentTimeMillis()
        // 1 条 1 小时前的崩溃（24 小时内，必须保留）
        repo.insert(crash("PreciousCrash", ts = realNow - 60 * 60 * 1000), meta)
        // 1200 条普通事件，触发 1000 条上限淘汰
        repeat(1200) { i ->
            val e = TelemetrySanitizer.build(
                TelemetryKind.API, "/api/books/$i", realNow + i.toLong(), durationMs = 10,
            )!!
            repo.insert(e, meta)
        }

        val rows = repo.all()
        assertTrue(rows.size <= TelemetryPolicy.MAX_ROWS + TelemetryPolicy.EVICT_CHUNK,
            "条数应被压到上限附近，实际 ${rows.size}")
        assertTrue(rows.any { it.name == "PreciousCrash" }, "24 小时内的崩溃绝不能被淘汰")
        driver.close()
        file.delete()
    }

    @Test
    fun cleanupDropsRecordsOlderThanSevenDays() {
        val file = newFile()
        val (driver, db) = open(file)
        val repo = TelemetryRepository(db)
        repo.insert(crash("Old", ts = now - 8L * 24 * 60 * 60 * 1000), meta)
        repo.insert(crash("New", ts = now), meta)
        val removed = repo.cleanup(now)
        assertEquals(1, removed)
        assertEquals(listOf("New"), repo.all().map { it.name })
        driver.close()
        file.delete()
    }

    @Test
    fun uploadsDeleteOnlyUploadedRows() {
        val file = newFile()
        val (driver, db) = open(file)
        val repo = TelemetryRepository(db)
        repo.insert(crash("A"), meta)
        repo.insert(crash("B"), meta)
        val batch = repo.pending()
        assertEquals(2, batch.size)
        repo.markUploaded(listOf(batch[0].id))
        assertEquals(1, repo.all().size)
        driver.close()
        file.delete()
    }
}
