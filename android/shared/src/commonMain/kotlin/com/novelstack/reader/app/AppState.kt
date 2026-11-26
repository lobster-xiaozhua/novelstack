package com.novelstack.reader.app

import com.novelstack.reader.crypto.ChapterCipher
import com.novelstack.reader.crypto.zeroBytes
import com.novelstack.reader.db.BookRow
import com.novelstack.reader.db.Books
import com.novelstack.reader.db.ReaderRepository
import com.novelstack.reader.db.SearchHit
import com.novelstack.reader.model.BookChange
import com.novelstack.reader.model.ApiException
import com.novelstack.reader.model.LoginRequest
import com.novelstack.reader.model.ProgressRequest
import com.novelstack.reader.net.NovelApi
import com.novelstack.reader.platform.AudioSinkFactory
import com.novelstack.reader.platform.DeviceKeyStore
import com.novelstack.reader.platform.LocalDataWiper
import com.novelstack.reader.tts.TtsController
import io.ktor.util.decodeBase64Bytes
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.datetime.Clock
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

sealed interface Screen {
    data object Login : Screen
    data object Library : Screen
    data object Search : Screen
    data object Settings : Screen
    data class Reader(val bookId: String) : Screen
}

@Serializable
data class ReaderSettings(
    val fontSize: Float = 18f,
    val lineHeight: Float = 1.7f,
    val background: String = "dark", // dark / paper / sepia / mint
    val pager: String = "slide", // slide / scroll
    val ttsSpeed: Float = 1.0f,
)

data class ReaderUiState(
    val book: Books? = null,
    val chapters: List<ReaderRepository.ChapterMeta> = emptyList(),
    val index: Int = 0,
    val title: String = "",
    val text: String = "",
    val fromCache: Boolean = false,
    val loading: Boolean = false,
    val message: String? = null,
) {
    val percent: Float
        get() = if (chapters.isEmpty()) 0f else (index + 1f) / chapters.size
}

/**
 * 应用状态机（单用户、单 Activity）。
 *
 * 所有网络调用都在 IO 侧；UI 只观察 StateFlow。解密后的正文只存在于
 * [ReaderUiState.text] 与 Compose 的文本布局中，任何方法都不写文件/日志。
 */
class AppState(
    private val api: NovelApi,
    private val keyStore: DeviceKeyStore,
    private val storeFactory: () -> ReaderRepository,
    private val wiper: LocalDataWiper,
    sinkFactory: AudioSinkFactory,
    private val json: Json,
    private val scope: CoroutineScope,
) {
    private var store: ReaderRepository = storeFactory()

    val screen = MutableStateFlow<Screen>(Screen.Login)
    val books = MutableStateFlow<List<Books>>(emptyList())
    val reader = MutableStateFlow(ReaderUiState())
    val searchResults = MutableStateFlow<List<SearchHit>>(emptyList())
    val busy = MutableStateFlow(false)
    val error = MutableStateFlow<String?>(null)
    val notice = MutableStateFlow<String?>(null)
    val serverUrl = MutableStateFlow("")
    val deviceId = MutableStateFlow("")
    val lastSyncAt = MutableStateFlow<Long>(0)

    val settings = MutableStateFlow(loadSettings())
    val tts = TtsController(api, keyStore, sinkFactory, scope)

    private var progressJob: Job? = null

    /**
     * 播放启动钩子：由平台层（Android MainActivity）设置，用于在开始朗读前
     * 拉起前台服务。commonMain 不引用任何 Android 类型。
     */
    var onPlaybackStarted: (() -> Unit)? = null

    /** UI 统一入口：先通知平台拉起前台服务，再开始朗读。 */
    fun ttsStart(bookId: String, chapterIndex: Int, speed: Float, fromSegment: Int = 0) {
        onPlaybackStarted?.invoke()
        tts.play(bookId, chapterIndex, speed, fromSegment)
    }

    init {
        // 播放通知/锁屏只展示书名与章节标题：由 AppState 提供元数据，TtsController 不接触正文。
        tts.setMetadataProvider {
            com.novelstack.reader.tts.PlaybackMetadata(
                bookTitle = reader.value.book?.title ?: "",
                chapterTitle = reader.value.title,
            )
        }
        tts.setChapterNavigation(object : com.novelstack.reader.tts.TtsController.ChapterNavigation {
            override fun next(current: Int): Int? {
                val n = current + 1
                return n.takeIf { it in 0 until reader.value.chapters.size }
            }
            override fun prev(current: Int): Int? {
                val p = current - 1
                return p.takeIf { it in 0 until reader.value.chapters.size }
            }
        })
        scope.launch { bootstrap() }
    }

    private fun loadSettings(): ReaderSettings = runCatching {
        val raw = store.kvGet("settings") ?: return@runCatching ReaderSettings()
        json.decodeFromString(ReaderSettings.serializer(), raw)
    }.getOrDefault(ReaderSettings())

    fun updateSettings(s: ReaderSettings) {
        settings.value = s
        store.kvPut("settings", json.encodeToString(ReaderSettings.serializer(), s))
    }

    private suspend fun bootstrap() {
        val server = store.kvGet("server_url")
        val token = store.kvGet("device_token")
        serverUrl.value = server ?: ""
        deviceId.value = store.kvGet("device_id") ?: ""
        if (server.isNullOrBlank() || token.isNullOrBlank()) {
            screen.value = Screen.Login
            return
        }
        api.configure(server, token)
        // 先展示本地书库，联网同步失败也能离线阅读。
        books.value = store.listBooks()
        try {
            syncInternal()
            screen.value = Screen.Library
        } catch (e: ApiException) {
            if (e.unauthorized) {
                clearAuth()
                screen.value = Screen.Login
                error.value = "设备凭证已失效，请重新配对"
            } else {
                screen.value = Screen.Library
                notice.value = "离线模式：${e.message}"
            }
        } catch (e: Throwable) {
            screen.value = Screen.Library
            notice.value = "离线模式：无法连接服务端"
        }
    }

    // ---- 登录 / 同步 ----

    fun login(
        server: String,
        username: String,
        password: String,
        pairingCode: String,
        deviceName: String,
    ) {
        if (busy.value) return
        error.value = null
        scope.launch {
            busy.value = true
            try {
                keyStore.ensureKeys()
                val normal = NovelApi.normalizeServer(server)
                val auth = api.loginDevice(
                    normal,
                    LoginRequest(
                        username = if (pairingCode.isBlank()) username else "",
                        password = if (pairingCode.isBlank()) password else "",
                        pairingCode = pairingCode,
                        deviceName = deviceName.ifBlank { "Android 阅读器" },
                        publicKey = keyStore.publicKeyPem(),
                    ),
                )
                api.configure(normal, auth.token)
                store.kvPut("server_url", normal)
                store.kvPut("device_token", auth.token)
                store.kvPut("device_id", auth.device.id)
                store.kvPut("device_name", auth.device.name)
                serverUrl.value = normal
                deviceId.value = auth.device.id
                syncInternal()
                screen.value = Screen.Library
            } catch (e: Throwable) {
                error.value = friendly(e)
            } finally {
                busy.value = false
            }
        }
    }

    fun sync() {
        if (busy.value) return
        scope.launch {
            busy.value = true
            try {
                syncInternal()
                notice.value = "同步完成：${books.value.size} 本"
            } catch (e: ApiException) {
                if (e.unauthorized) {
                    clearAuth()
                    screen.value = Screen.Login
                }
                error.value = friendly(e)
            } catch (e: Throwable) {
                error.value = friendly(e)
            } finally {
                busy.value = false
            }
        }
    }

    private suspend fun syncInternal() {
        var since = store.kvGet("cursor")?.toLongOrNull() ?: 0L
        var m = api.manifest(since)
        if (m.resetRequired) {
            store.clearBooks()
            since = 0
            m = api.manifest(0)
        }
        if (m.books.isNotEmpty()) {
            store.upsertBooks(m.books.map { it.toRow() })
            for (b in m.books) {
                store.upsertChapterMeta(
                    b.id,
                    b.chapters.map { Triple(it.index, it.title, it.etag) },
                )
            }
        }
        for (id in m.deleted) store.deleteBook(id)
        store.kvPut("cursor", m.cursor.toString())
        books.value = store.listBooks()
        lastSyncAt.value = Clock.System.now().toEpochMilliseconds()
        flushProgress()
    }

    private fun BookChange.toRow(): BookRow = BookRow(
            id = id,
            title = title,
            author = author,
            tags = tags.joinToString(","),
            description = description,
            chapterCount = chapterCount,
            sizeBytes = sizeBytes,
            sha256 = sha256,
            chunkSize = chunkSize,
            kekVersion = kekVersion,
            syncSeq = syncSeq,
            wrappedDek = wrappedDek,
            updatedAt = Clock.System.now().toEpochMilliseconds(),
    )

    // ---- 阅读 ----

    fun openBook(bookId: String) {
        val book = store.getBook(bookId) ?: return
        val meta = store.chaptersMeta(bookId)
        val saved = store.localProgress(bookId)
        var idx = saved?.chapter_index?.toInt() ?: 0
        if (idx !in meta.indices) idx = 0
        reader.value = ReaderUiState(
            book = book,
            chapters = meta,
            index = idx,
            title = meta.getOrNull(idx)?.title ?: "",
            loading = true,
        )
        screen.value = Screen.Reader(bookId)
        scope.launch { loadChapter(bookId, idx, report = false) }
        if (saved == null) restoreRemoteProgress(bookId)
    }

    /**
     * 本机没有进度时（新设备、重装、刚清过数据）向服务端续读。
     * 失败一律静默：离线也照常从第一章开始，不能因为“取进度”打断阅读。
     */
    private fun restoreRemoteProgress(bookId: String) {
        scope.launch {
            val remote = runCatching { api.getProgress(bookId) }.getOrNull() ?: return@launch
            val cur = reader.value
            if ((screen.value as? Screen.Reader)?.bookId != bookId) return@launch
            // 用户已经自己翻页了就不要抢方向盘。
            if (cur.index != 0 || cur.chapters.size < 2) return@launch
            val target = remote.chapterIndex.coerceIn(0, cur.chapters.size - 1)
            if (target == 0) return@launch
            loadChapter(bookId, target, report = false)
            store.saveProgress(bookId, target, remote.charOffset, percentOf(cur.chapters.size, target))
            notice.value = "已恢复阅读位置：第 ${target + 1} 章"
        }
    }

    fun openAt(bookId: String, chapterIndex: Int) {
        val book = store.getBook(bookId) ?: return
        val meta = store.chaptersMeta(bookId)
        val idx = chapterIndex.coerceIn(0, maxOf(0, meta.size - 1))
        reader.value = ReaderUiState(
            book = book, chapters = meta, index = idx,
            title = meta.getOrNull(idx)?.title ?: "", loading = true,
        )
        screen.value = Screen.Reader(bookId)
        scope.launch { loadChapter(bookId, idx) }
    }

    fun goChapter(index: Int) {
        val bookId = (screen.value as? Screen.Reader)?.bookId ?: return
        val total = reader.value.chapters.size
        if (index < 0 || index >= total) return
        scope.launch { loadChapter(bookId, index) }
    }

    private suspend fun loadChapter(bookId: String, index: Int, report: Boolean = true) {
        val meta = store.chaptersMeta(bookId)
        reader.value = reader.value.copy(
            chapters = meta, index = index, loading = true, message = null,
            title = meta.getOrNull(index)?.title ?: reader.value.title,
        )
        val cached = store.getChapter(bookId, index)
        if (cached != null && cached.text.isNotEmpty()) {
            reader.value = reader.value.copy(
                title = cached.title, text = cached.text, fromCache = true, loading = false,
            )
            if (report) reportProgress(bookId, index, 0, percentOf(meta.size, index))
            return
        }
        try {
            prefetchInternal(bookId, listOf(index))
            val c = store.getChapter(bookId, index)
            if (c == null || c.text.isEmpty()) throw IllegalStateException("章节内容为空")
            reader.value = reader.value.copy(
                title = c.title, text = c.text, fromCache = false, loading = false,
            )
            if (report) reportProgress(bookId, index, 0, percentOf(meta.size, index))
        } catch (e: Throwable) {
            reader.value = reader.value.copy(
                loading = false,
                message = "本章未缓存，且无法联网获取（${friendly(e)}）",
            )
        }
    }

    /** 预取当前章节之后的 3 章（离线阅读的核心动作）。 */
    fun prefetchAhead() {
        val bookId = (screen.value as? Screen.Reader)?.bookId ?: return
        val r = reader.value
        val start = r.index
        val end = minOf(r.chapters.size, start + 3)
        if (start >= end) return
        val need = (start until end).filter { idx -> r.chapters.getOrNull(idx)?.cached != true }
        if (need.isEmpty()) {
            notice.value = "接下来 3 章已在本地"
            return
        }
        scope.launch {
            busy.value = true
            try {
                prefetchInternal(bookId, need)
                notice.value = "已缓存 ${need.size} 章（密文传输，本地解密）"
            } catch (e: Throwable) {
                error.value = friendly(e)
            } finally {
                busy.value = false
            }
        }
    }

    private suspend fun prefetchInternal(bookId: String, indexes: List<Int>) {
        if (indexes.isEmpty()) return
        val resp = api.prefetch(bookId, indexes)
        if (resp.wrappedDek.isEmpty()) {
            throw IllegalStateException("服务端未返回设备可解的密钥（请重新配对设备）")
        }
        val dek = keyStore.unwrapDEK(resp.wrappedDek)
        try {
            for (ch in resp.chapters) {
                val pkg = ch.ciphertext.decodeBase64Bytes()
                val plain = ChapterCipher.open(dek, bookId, ch.index, pkg)
                try {
                    store.putChapter(bookId, ch.index, ch.title, ch.etag, plain.decodeToString())
                } finally {
                    zeroBytes(plain)
                }
            }
        } finally {
            zeroBytes(dek)
        }
        reader.value = reader.value.copy(chapters = store.chaptersMeta(bookId))
    }

    fun reportProgress(bookId: String, chapterIndex: Int, charOffset: Int, percent: Float) {
        store.saveProgress(bookId, chapterIndex, charOffset, percent)
        progressJob?.cancel()
        progressJob = scope.launch {
            delay(800) // 防抖：快速翻页只上报最后一次
            try {
                api.putProgress(
                    bookId,
                    ProgressRequest(
                        chapterIndex = chapterIndex,
                        charOffset = charOffset,
                        percent = percent.toDouble().coerceIn(0.0, 1.0),
                        deviceId = store.kvGet("device_id") ?: "",
                    ),
                )
                store.markProgressSynced(bookId)
            } catch (e: ApiException) {
                if (e.unauthorized) {
                    clearAuth()
                    screen.value = Screen.Login
                }
                // 其他错误：保留本地未同步状态，下次 sync 重试
            } catch (_: Throwable) {
                // 离线：保留本地
            }
        }
    }

    private suspend fun flushProgress() {
        for (p in store.unsyncedProgress()) {
            try {
                api.putProgress(
                    p.book_id,
                    ProgressRequest(
                        chapterIndex = p.chapter_index.toInt(),
                        charOffset = p.char_offset.toInt(),
                        percent = p.percent,
                        deviceId = store.kvGet("device_id") ?: "",
                    ),
                )
                store.markProgressSynced(p.book_id)
            } catch (_: Throwable) {
                return // 网络不可用就停止，下次再试
            }
        }
    }

    // ---- 搜索 ----

    fun search(query: String) {
        scope.launch {
            searchResults.value = store.search(query)
        }
    }

    // ---- 退出并清除 ----

    fun logoutAndWipe() {
        scope.launch {
            tts.stop()
            progressJob?.cancel()
            runCatching { store.wipe() }
            runCatching { store.close() }
            wiper.wipeAll()
            keyStore.wipe()
            store = storeFactory()
            api.configure("", null)
            books.value = emptyList()
            reader.value = ReaderUiState()
            searchResults.value = emptyList()
            serverUrl.value = ""
            deviceId.value = ""
            lastSyncAt.value = 0
            settings.value = ReaderSettings()
            screen.value = Screen.Login
            notice.value = "本地数据已清除，需重新配对"
        }
    }

    private fun clearAuth() {
        store.kvDelete("device_token")
        api.configure(serverUrl.value, null)
        books.value = store.listBooks()
    }

    /** 本地检索能力（UI 展示用）：trigram / unicode61 / LIKE 回退。 */
    fun ftsModeLabel(): String = when (store.ftsMode) {
        com.novelstack.reader.db.FtsMode.TRIGRAM -> "FTS5 trigram"
        com.novelstack.reader.db.FtsMode.UNICODE61 -> "FTS5 unicode61"
        com.novelstack.reader.db.FtsMode.NONE -> "LIKE 回退（FTS5 不可用）"
    }

    private fun percentOf(total: Int, index: Int): Float =
        if (total <= 0) 0f else ((index + 1).toFloat() / total).coerceIn(0f, 1f)

    private fun friendly(e: Throwable): String = when (e) {
        is ApiException -> when {
            e.rateLimited -> "请求过于频繁（服务端限流），请稍后再试"
            e.forbidden -> "该操作需要浏览器会话（设备令牌无权访问）"
            e.unauthorized -> "凭证无效或已被撤销"
            else -> "${e.message}（HTTP ${e.status}）"
        }
        else -> e.message ?: "未知错误"
    }
}
