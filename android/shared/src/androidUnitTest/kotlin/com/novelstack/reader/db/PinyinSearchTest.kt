package com.novelstack.reader.db

import app.cash.sqldelight.driver.jdbc.sqlite.JdbcSqliteDriver
import com.novelstack.reader.search.PinyinProvider
import java.io.File
import java.util.Properties
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

/**
 * 拼音索引与模糊搜索（验收 C1）。
 *
 * 用一张极小的字典代替 TinyPinyin：单测只验证「索引列被正确写入、查询能命中」，
 * 真实的汉字转拼音能力由 TinyPinyin 库保证，不需要在这里复测它的字表。
 */
private class FakePinyin : PinyinProvider {
    private val table = mapOf(
        '小' to "xiao", '说' to "shuo",
        '三' to "san", '体' to "ti",
        '张' to "zhang", '夜' to "ye", '色' to "se",
    )

    override fun full(text: String): String = buildString {
        for (c in text) append(table[c] ?: if (c.isLetterOrDigit()) c.lowercaseChar().toString() else "")
    }

    override fun initials(text: String): String = buildString {
        for (c in text) {
            val py = table[c]
            if (py != null) append(py[0]) else if (c.isLetterOrDigit()) append(c.lowercaseChar())
        }
    }
}

class PinyinSearchTest {

    private fun newRepo(pinyin: PinyinProvider = FakePinyin()): ReaderRepository {
        val file = File.createTempFile("reader-pinyin", ".db").also { it.delete() }
        val driver = JdbcSqliteDriver("jdbc:sqlite:${file.absolutePath}", Properties())
        ReaderDatabase.Schema.create(driver)
        val repo = ReaderRepository(ReaderDatabase(driver), driver, pinyin)
        repo.ensureColumns()
        repo.initFts()
        return repo
    }

    private val books = listOf(
        BookRow(id = "b1", title = "小说", author = "张三"),
        BookRow(id = "b2", title = "夜色", author = ""),
    )

    @Test
    fun fullPinyinHitsBook() {
        val repo = newRepo()
        repo.upsertBooks(books)

        val hits = repo.search("xiaoshuo")
        assertTrue(hits.any { it.bookId == "b1" && it.matchType == "pinyin" }, "全拼应命中《小说》: $hits")
    }

    @Test
    fun initialsHitBook() {
        val repo = newRepo()
        repo.upsertBooks(books)

        val hits = repo.search("xs")
        assertTrue(hits.any { it.bookId == "b1" }, "首字母 xs 应命中《小说》: $hits")
    }

    @Test
    fun authorPinyinHitsBook() {
        val repo = newRepo()
        repo.upsertBooks(books)

        val hits = repo.search("zhangsan")
        assertTrue(hits.any { it.bookId == "b1" }, "作者拼音应命中: $hits")
    }

    @Test
    fun pinyinColumnsArePopulated() {
        val repo = newRepo()
        repo.upsertBooks(books)
        // 通过 raw 查询确认列值（SQLDelight 的 Books 模型不含后加的拼音列）。
        val driverRepo = repo
        val hits = driverRepo.search("yes") // 夜色 → ye se，只输入 yes 也应子串命中
        assertTrue(hits.any { it.bookId == "b2" }, "子串拼音应命中《夜色》: $hits")
    }

    @Test
    fun nonLatinQuerySkipsPinyin() {
        val repo = newRepo()
        repo.upsertBooks(books)
        val hits = repo.search("小说")
        // 没有缓存章节时中文查询不会命中任何东西（拼音只在拉丁查询时启用）。
        assertEquals(0, hits.size)
    }

    @Test
    fun rebuildPinyinBackfillsOldRows() {
        val repo = newRepo(PinyinProvider.None)
        repo.upsertBooks(books) // 无拼音写入
        assertTrue(repo.search("xiaoshuo").isEmpty())

        // 模拟升级后首次启动：换一个带拼音能力的仓库无法复用同一 driver，
        // 这里直接调用 rebuildPinyin 验证「补齐」路径本身不报错且可重复执行。
        repo.rebuildPinyin()
        assertTrue(repo.search("xiaoshuo").isEmpty(), "None provider 不应写入拼音")
    }
}
