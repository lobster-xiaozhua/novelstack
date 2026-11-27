package com.novelstack.reader.db

import app.cash.sqldelight.db.SqlDriver
import kotlinx.datetime.Clock

/** FTS 能力：trigram 最适合中文子串，unicode61 作为退路，NONE 时用 LIKE。 */
enum class FtsMode { NONE, UNICODE61, TRIGRAM }

data class SearchHit(
    val bookId: String,
    val bookTitle: String,
    val chapterIndex: Int,
    val chapterTitle: String,
    val snippet: String,
)

/**
 * 本地加密库的读写入口。
 *
 * 安全边界：`text` 只在这里与 Compose 之间传递；不写日志、不写外部文件、
 * 不进崩溃报告。数据库由 SQLCipher 整库加密（密钥由 Keystore 包装）。
 */
class ReaderRepository(
    private val db: ReaderDatabase,
    private val driver: SqlDriver,
) {
    private val q = db.readerDatabaseQueries
    private val raw = RawDb(driver)

    var ftsMode: FtsMode = FtsMode.NONE
        private set

    fun initFts(): FtsMode {
        ftsMode = try {
            raw.exec(
                """CREATE VIRTUAL TABLE IF NOT EXISTS chapters_fts USING fts5(
                    text, title, book_id UNINDEXED, chapter_index UNINDEXED,
                    tokenize='trigram')""",
            )
            FtsMode.TRIGRAM
        } catch (first: Throwable) {
            try {
                raw.exec(
                    """CREATE VIRTUAL TABLE IF NOT EXISTS chapters_fts USING fts5(
                        text, title, book_id UNINDEXED, chapter_index UNINDEXED,
                        tokenize='unicode61')""",
                )
                FtsMode.UNICODE61
            } catch (second: Throwable) {
                FtsMode.NONE
            }
        }
        return ftsMode
    }

    fun upsertBooks(books: List<BookRow>) {
        for (b in books) {
            q.insertBook(
                id = b.id, title = b.title, author = b.author, tags = b.tags,
                description = b.description, chapter_count = b.chapterCount.toLong(),
                size_bytes = b.sizeBytes, sha256 = b.sha256, chunk_size = b.chunkSize.toLong(),
                kek_version = b.kekVersion.toLong(), sync_seq = b.syncSeq,
                wrapped_dek = b.wrappedDek, updated_at = b.updatedAt,
            )
        }
    }

    fun listBooks(): List<Books> = q.selectBooks().executeAsList()

    /** 老库升级：SQLDelight 未配置版本迁移，这里对后加的列做幂等 ALTER（已存在则忽略）。 */
    fun ensureColumns() {
        runCatching {
            raw.exec("ALTER TABLE books ADD COLUMN is_favorite INTEGER NOT NULL DEFAULT 0")
        }
    }

    fun favoriteBooks(): List<Books> = q.selectFavoriteBooks().executeAsList()

    fun setFavorite(bookId: String, favorite: Boolean) {
        q.updateFavorite(if (favorite) 1L else 0L, bookId)
    }

    fun isFavorite(bookId: String): Boolean =
        q.selectFavoriteFlag(bookId).executeAsOneOrNull()?.let { it != 0L } ?: false

    fun getBook(id: String): Books? = q.selectBook(id).executeAsOneOrNull()

    fun deleteBook(id: String) {
        q.deleteChapters(id)
        if (ftsMode != FtsMode.NONE) {
            raw.exec("DELETE FROM chapters_fts WHERE book_id = ?", listOf(id))
        }
        q.deleteBook(id)
    }

    fun clearBooks() {
        q.deleteAllChapters()
        if (ftsMode != FtsMode.NONE) raw.exec("DELETE FROM chapters_fts")
        q.clearAll()
    }

    fun putChapter(bookId: String, index: Int, title: String, etag: String, text: String) {
        val ts = now()
        q.upsertChapter(bookId, index.toLong(), title, etag, text, ts)
        if (ftsMode != FtsMode.NONE) {
            raw.exec(
                "DELETE FROM chapters_fts WHERE book_id = ? AND chapter_index = ?",
                listOf(bookId, index),
            )
            val rowid = raw.long(
                "SELECT id FROM chapters WHERE book_id = ? AND chapter_index = ?",
                listOf(bookId, index),
            ) ?: return
            raw.exec(
                "INSERT INTO chapters_fts(rowid, text, title, book_id, chapter_index) VALUES (?,?,?,?,?)",
                listOf(rowid, text, title, bookId, index),
            )
        }
    }

    fun getChapter(bookId: String, index: Int): Chapters? =
        q.selectChapter(bookId, index.toLong()).executeAsOneOrNull()

    /** 同步目录时写入标题/ETag，不覆盖已缓存的正文。 */
    fun upsertChapterMeta(bookId: String, chapters: List<Triple<Int, String, String>>) {
        for ((index, title, etag) in chapters) {
            q.upsertChapterMeta(bookId, index.toLong(), title, etag)
        }
    }

    data class ChapterMeta(val index: Int, val title: String, val etag: String, val cached: Boolean)

    fun chaptersMeta(bookId: String): List<ChapterMeta> =
        q.selectChapterMeta(bookId).executeAsList().map {
            ChapterMeta(
                index = it.chapter_index.toInt(),
                title = it.title,
                etag = it.etag,
                cached = it.cached,
            )
        }

    fun chapterIndexes(bookId: String): List<Int> =
        q.selectChapterIndexes(bookId).executeAsList().map { it.toInt() }

    fun countChapters(bookId: String): Int = chapterIndexes(bookId).size

    /** 最近阅读章节：用于打开书时恢复位置。 */
    fun lastChapter(bookId: String): Int = chapterIndexes(bookId).maxOrNull() ?: 0

    fun search(query: String, limit: Int = 50): List<SearchHit> {
        val needle = query.trim()
        if (needle.isEmpty()) return emptyList()
        val hits = mutableListOf<SearchHit>()
        if (ftsMode == FtsMode.TRIGRAM && needle.length >= 3) {
            val expr = "\"" + needle.replace("\"", "\"\"") + "\""
            val rows = raw.query(
                """SELECT f.book_id, f.chapter_index, f.title,
                          snippet(chapters_fts, 0, '【', '】', '…', 16),
                          COALESCE(b.title, '')
                   FROM chapters_fts f LEFT JOIN books b ON b.id = f.book_id
                   WHERE chapters_fts MATCH ?
                   ORDER BY rank LIMIT ?""",
                listOf(expr, limit),
            ) { c ->
                SearchHit(
                    bookId = c.getString(0) ?: "",
                    chapterIndex = (c.getLong(1) ?: 0L).toInt(),
                    chapterTitle = c.getString(2) ?: "",
                    snippet = c.getString(3) ?: "",
                    bookTitle = c.getString(4) ?: "",
                )
            }
            hits.addAll(rows)
        }
        if (hits.isEmpty()) {
            val like = "%" + escapeLike(needle) + "%"
            val rows = raw.query(
                """SELECT c.book_id, c.chapter_index, c.title,
                          substr(c.text, 1, 160), COALESCE(b.title, '')
                   FROM chapters c LEFT JOIN books b ON b.id = c.book_id
                   WHERE c.text LIKE ? ESCAPE '\' OR c.title LIKE ? ESCAPE '\'
                   ORDER BY c.chapter_index LIMIT ?""",
                listOf(like, like, limit),
            ) { c ->
                SearchHit(
                    bookId = c.getString(0) ?: "",
                    chapterIndex = (c.getLong(1) ?: 0L).toInt(),
                    chapterTitle = c.getString(2) ?: "",
                    snippet = highlight(c.getString(3) ?: "", needle),
                    bookTitle = c.getString(4) ?: "",
                )
            }
            hits.addAll(rows)
        }
        return hits
    }

    fun saveProgress(bookId: String, chapterIndex: Int, charOffset: Int, percent: Float) {
        val ts = now()
        val existing = q.selectProgress(bookId).executeAsOneOrNull()
        q.upsertProgress(
            book_id = bookId,
            chapter_index = chapterIndex.toLong(),
            char_offset = charOffset.toLong(),
            percent = percent.toDouble(),
            updated_at = ts,
            // 保留上次同步时间：只有内容变化才需要重新上报。
            synced_at = existing?.synced_at ?: 0L,
        )
    }

    fun localProgress(bookId: String): Progress? = q.selectProgress(bookId).executeAsOneOrNull()

    fun markProgressSynced(bookId: String) {
        q.markProgressSynced(now(), bookId)
    }

    fun unsyncedProgress(limit: Long = 20): List<Progress> =
        q.selectUnsyncedProgress(limit).executeAsList()

    fun kvGet(key: String): String? = q.kvGet(key).executeAsOneOrNull()

    fun kvPut(key: String, value: String) = q.kvPut(key, value)

    fun kvDelete(key: String) = q.kvDelete(key)

    /** 清空全部本地数据（退出登录时调用；调用方随后关闭 driver 并删除文件）。 */
    fun wipe() {
        q.deleteAllChapters()
        if (ftsMode != FtsMode.NONE) raw.exec("DELETE FROM chapters_fts")
        q.clearAll()
        q.kvDelete("device_token")
        q.kvDelete("server_url")
        q.kvDelete("cursor")
        raw.exec("DELETE FROM progress")
        raw.exec("VACUUM")
    }

    fun close() = driver.close()

    private fun now(): Long = Clock.System.now().toEpochMilliseconds()

    private fun escapeLike(s: String): String =
        s.replace("\\", "\\\\").replace("%", "\\%").replace("_", "\\_")

    /** LIKE 回退时在客户端做一次简单高亮（转义后只标第一个命中）。 */
    private fun highlight(text: String, needle: String): String {
        val i = text.indexOf(needle, ignoreCase = true)
        if (i < 0) return text
        return text.substring(0, i) + "【" + text.substring(i, i + needle.length) + "】" +
            text.substring(i + needle.length)
    }
}

/** 书籍的本地模型（与 SQLDelight 的 books 表解耦，便于上层构造）。 */
data class BookRow(
    val id: String,
    val title: String,
    val author: String = "",
    val tags: String = "",
    val description: String = "",
    val chapterCount: Int = 0,
    val sizeBytes: Long = 0,
    val sha256: String = "",
    val chunkSize: Int = 0,
    val kekVersion: Int = 1,
    val syncSeq: Long = 0,
    val wrappedDek: String = "",
    val updatedAt: Long = Clock.System.now().toEpochMilliseconds(),
)
