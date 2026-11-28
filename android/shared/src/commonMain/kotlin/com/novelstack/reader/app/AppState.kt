package com.novelstack.reader.app

import com.novelstack.reader.crypto.ChapterCipher
import com.novelstack.reader.crypto.zeroBytes
import com.novelstack.reader.db.BookRow
import com.novelstack.reader.db.Books
import com.novelstack.reader.db.ReaderRepository
import com.novelstack.reader.db.SearchHit
import com.novelstack.reader.model.BookChange
import com.novelstack.reader.model.ApiException
import com.novelstack.reader.model.AppVersion
import com.novelstack.reader.model.DiscoverResponse
import com.novelstack.reader.model.HistoryItem
import com.novelstack.reader.model.WebGrantDto
import com.novelstack.reader.model.LoginRequest
import com.novelstack.reader.model.ProgressRequest
import com.novelstack.reader.model.ReadingStats
import com.novelstack.reader.net.NetworkCircuit
import com.novelstack.reader.net.NovelApi
import com.novelstack.reader.platform.AudioSinkFactory
import com.novelstack.reader.platform.DeviceKeyStore
import com.novelstack.reader.platform.LocalDataWiper
import com.novelstack.reader.telemetry.Telemetry
import com.novelstack.reader.telemetry.TelemetryKind
import com.novelstack.reader.tts.TtsController
import com.novelstack.reader.update.AppUpdater
import com.novelstack.reader.update.UpdatePlanner
import io.ktor.util.decodeBase64Bytes
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch
import kotlinx.datetime.Clock
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json

sealed interface Screen {
    data object Login : Screen
    data object Library : Screen
    data object Discover : Screen
    data object History : Screen
    data object Search : Screen
    data object Settings : Screen
    data object Stats : Screen
    data object ServerSettings : Screen
    data class Reader(val bookId: String) : Screen
    /** 内嵌 WebView 打开控制台（已带一次性登录票据）。 */
    data class WebConsole(val url: String) : Screen
}

@Serializable
data class ReaderSettings(
    val fontSize: Float = 18f,
    val lineHeight: Float = 1.7f,
    // 第八阶段：四主题。auto=跟随系统 / light=白天 / dark=夜间 / eye=护眼(黄底)
    val theme: String = "auto",
    // 兼容旧版字段：theme 未覆盖时作为回退纸张色（dark/paper/sepia/mint）
    val background: String = "dark",
    // 翻页：slide=平移(默认) / cover=覆盖 / scroll=上下滚动
    val pager: String = "slide",
    // 第九阶段：隐藏刘海（阅读页绘制到挖孔区域，最大化可用宽度）
    val hideNotch: Boolean = false,
    val ttsSpeed: Float = 1.0f,

    // ---- 终端防御阶段 ----
    /** 生物识别应用锁开关（需设备支持且已录入；否则强制关闭）。 */
    val appLockEnabled: Boolean = false,
    /** 防截屏开关：阅读页/设置页加 FLAG_SECURE，防截屏/录屏/最近任务缩略图。 */
    val secureScreen: Boolean = true,
    /** 翻页触感开关：低电量时由 HapticPolicy 自动抑制。 */
    val hapticsEnabled: Boolean = true,
    /** 移动网络下发现页封面是否只加载低清占位（默认省流量）。 */
    val lowDataCover: Boolean = true,
)

/** 检查更新 UI 状态（只含版本元数据与下载进度，不含 APK 字节）。 */
data class UpdateUiState(
    val checking: Boolean = false,
    val info: AppVersion? = null,
    val hasUpdate: Boolean = false,
    val downloading: Boolean = false,
    val progress: Float = 0f,
    val message: String? = null,
    val readyToInstall: String? = null,
)

/**
 * 离线进度提示。
 *
 * 触发条件：本机有「未同步」的阅读进度，且服务端记录的章节比本地更靠前（章节索引更小）。
 * 说明用户在离线期间读得更远，需要问一句要不要跳过去，而不是默默覆盖。
 */
data class OfflineProgressPrompt(
    val bookId: String,
    val localChapter: Int,
    val remoteChapter: Int,
    val totalChapters: Int,
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
    private val appUpdater: AppUpdater? = null,
    /** 第九阶段：系统保活引导（Android 实现；iOS 传 null）。 */
    private val keepAlive: KeepAliveController? = null,
    /** 第九阶段：遥测统计/清空/立即上传。 */
    private val telemetryControl: TelemetryControl? = null,
    /** 第十阶段：局域网服务发现（Android 实现；其它平台传 null）。 */
    private val lanDiscovery: LanDiscoveryController? = null,
    /** 终端防御阶段：设备侧能力（Android 实现；其它平台用 Noop 默认）。 */
    private val powerMonitor: PowerMonitor = NoopPowerMonitor,
    private val appLock: AppLockController = NoopAppLock,
    private val screenGuard: ScreenGuard = NoopScreenGuard,
    private val textActions: TextActions = NoopTextActions,
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

    // ---- 第八阶段：发现 / 足迹 / 收藏 / 更新 ----
    val discover = MutableStateFlow<DiscoverResponse?>(null)
    val history = MutableStateFlow<List<HistoryItem>>(emptyList())
    /** 书架筛选：true=只看收藏。 */
    val favoritesOnly = MutableStateFlow(false)
    val update = MutableStateFlow(UpdateUiState())

    // ---- 第九阶段：网络状态与遥测 ----
    /** 供顶部横幅与预读开关使用；由平台 NetworkCallback 驱动。 */
    val network = MutableStateFlow(NetworkStatus.Online)

    // ---- 第九阶段（本期）：离线熔断、拼音、阅读统计 ----
    /** 连续 3 次 API 超时后为 true；由网络拦截器驱动。 */
    val offline = NetworkCircuit.offline
    /** 有本地缓存的书籍 id：离线模式下书架只展示这些。 */
    val cachedIds = MutableStateFlow<Set<String>>(emptySet())
    /** 阅读统计（进入统计页时刷新）。 */
    val readingStats = MutableStateFlow<ReadingStats?>(null)
    /** 离线进度提示：本地进度比云端更靠后时弹出，等用户决定。 */
    val offlinePrompt = MutableStateFlow<OfflineProgressPrompt?>(null)
    val telemetryStats = MutableStateFlow<com.novelstack.reader.telemetry.TelemetryStats?>(null)

    // ---- 第十阶段：局域网发现与动态根地址 ----
    /** 发现到的局域网服务列表（NsdManager + UDP 广播）。 */
    val lanServices = MutableStateFlow<List<com.novelstack.reader.model.LanService>>(emptyList())
    /** 当前实际使用的节点是否为内网（供设置页展示）。 */
    val usingLocalEndpoint = MutableStateFlow(false)
    /** 服务器设置页的操作反馈。 */
    val endpointMessage = MutableStateFlow<String?>(null)

    // ---- 终端防御阶段：安全 / 能耗 ----
    /** 电池快照；由平台 PowerMonitor 驱动。 */
    val power = MutableStateFlow(powerMonitor.current())
    /** 应用锁：true 时全屏遮罩，必须验证通过才能使用。 */
    val locked = MutableStateFlow(false)
    /** 当前页面是否需要 FLAG_SECURE（阅读页/设置页）。 */
    val secureNow = MutableStateFlow(false)

    /** 只有不计量网络、且电量不低时允许预读/批量同步。 */
    val canPrefetch: Boolean get() = network.value == NetworkStatus.Online && !power.value.isLow

    private var progressJob: Job? = null
    /** 前台 5 分钟同步循环（进入后台即取消）。 */
    private var syncLoopJob: Job? = null
    /** 电量监听注销句柄（进程退出时释放）。 */
    private var powerUnregister: (() -> Unit)? = null
    /** 上次进入后台的单调时钟（毫秒）；0 表示在前台。 */
    private var backgroundSinceMs: Long = 0L
    /** 本次锁定是否由「进入后台」触发（用于 <30s 回前台时自动放行）。 */
    private var lockOnBackground: Boolean = false
    /** 上次触觉反馈时刻，用于 HapticPolicy 节流。 */
    private var lastHapticAtMs: Long = 0L

    /** 上次计入阅读时长的时间点（毫秒）；0 表示尚未开始计时。 */
    private var lastActivityAt: Long = 0L
    /** 已累计但尚未上报的阅读秒数。 */
    private var pendingReadSeconds: Int = 0

    /**
     * 取出累计的阅读秒数并清零。
     * 上报失败时这部分会丢——阅读统计属于非关键数据，不值得为它加重试队列。
     */
    private fun drainReadingSeconds(): Int {
        val now = Clock.System.now().toEpochMilliseconds()
        if (lastActivityAt > 0L) {
            val delta = ((now - lastActivityAt) / 1000L).toInt().coerceIn(0, 120)
            pendingReadSeconds = (pendingReadSeconds + delta).coerceAtMost(600)
        }
        lastActivityAt = now
        val out = pendingReadSeconds
        pendingReadSeconds = 0
        return out
    }

    /**
     * 播放启动钩子：由平台层（Android MainActivity）设置，用于在开始朗读前
     * 拉起前台服务。commonMain 不引用任何 Android 类型。
     */
    var onPlaybackStarted: (() -> Unit)? = null

    /** 亮度调节钩子：由 Android 平台层设置（android.view.Window#screenBrightness）。 */
    var onBrightnessChange: ((Float) -> Unit)? = null

    /**
     * 二维码扫描钩子：由 Android MainActivity 设置（ZXing CaptureActivity）。
     * commonMain 只声明「扫到文本后回调」，不引用任何平台类型。
     */
    var onScanQr: ((success: (String) -> Unit) -> Unit)? = null

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
        // 终端防御：注册电量监听，同步初始快照。
        powerUnregister = powerMonitor.register { snapshot ->
            val wasLow = power.value.isLow
            power.value = snapshot
            // 低电量：缩小 TTS 缓冲；预读由 canPrefetch 拦截，遥测重试由 worker 拦截。
            com.novelstack.reader.app.AudioBufferPolicy.lowPower = snapshot.isLow
            if (snapshot.isLow && !wasLow) {
                notice.value = "电量较低或省电模式：已暂停后台预读与触感反馈"
            }
        }
        com.novelstack.reader.app.AudioBufferPolicy.lowPower = power.value.isLow
        // 应用锁：支持且已录入时，冷启动立即要求验证。
        if (settings.value.appLockEnabled && appLock.isAvailable() && appLock.isEnrolled()) {
            locked.value = true
        }
        scope.launch {
            bootstrap()
            refreshCachedIds()
        }
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
        // 第十阶段：优先从 ApiEndpointManager 读；首次升级时从旧 kv 迁移一次。
        val mgr = com.novelstack.reader.net.ApiEndpointManager
        val server = mgr.primaryUrl().ifBlank { store.kvGet("server_url") ?: "" }
        val token = mgr.deviceToken().ifBlank { store.kvGet("device_token") ?: "" }
        val localUrl = mgr.localUrl()
        if (server.isNotBlank() && mgr.primaryUrl().isBlank()) {
            // 迁移：旧版本只有 kv，没有 endpoint 配置。
            mgr.configure(server, "", token)
        }
        serverUrl.value = server
        deviceId.value = store.kvGet("device_id") ?: ""
        Telemetry.updateDeviceId(deviceId.value)
        usingLocalEndpoint.value = mgr.isLocalActive()
        if (server.isBlank() || token.isBlank()) {
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
                // 第十阶段：保留已有内网地址（若有），只更新主地址与 token。
                val mgr = com.novelstack.reader.net.ApiEndpointManager
                val localUrl = mgr.localUrl()
                mgr.configure(normal, localUrl, auth.token)
                api.configure(normal, auth.token)
                store.kvPut("server_url", normal)
                store.kvPut("device_token", auth.token)
                store.kvPut("device_id", auth.device.id)
                store.kvPut("device_name", auth.device.name)
                serverUrl.value = normal
                deviceId.value = auth.device.id
                // 遥测从此刻起带上设备标识（崩溃发生在登录前也能上传，只是 device_id 为空）。
                Telemetry.updateDeviceId(auth.device.id)
                Telemetry.requestUpload()
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
        refreshCachedIds()
        refreshEndpointState()
        flushProgress()
        // 把其它端更靠后的进度拉回来（失败不影响书架同步）。
        runCatching { pullProgress() }
    }

    /**
     * 增量拉取远端进度并落本地。
     *
     * 只在远端更靠后时才覆盖本地：否则正在阅读的这一端会被别的端的回翻打断。
     * 覆盖后标记为已同步，避免回环上报。
     */
    private suspend fun pullProgress() {
        if (!api.hasToken()) return
        val since = store.kvGet("progress_cursor")?.takeIf { it.isNotBlank() }
        val resp = api.syncProgress(since)
        var changed = false
        for (p in resp.items) {
            val local = store.localProgress(p.bookId)
            val ahead = local == null ||
                p.chapterIndex > local.chapter_index.toInt() ||
                (p.chapterIndex == local.chapter_index.toInt() && p.charOffset > local.char_offset.toInt())
            if (ahead) {
                store.applyRemoteProgress(p.bookId, p.chapterIndex, p.charOffset, p.percent)
                changed = true
            }
        }
        if (resp.cursor.isNotBlank()) store.kvPut("progress_cursor", resp.cursor)
        if (changed) books.value = store.listBooks()
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
        if (saved == null) {
            restoreRemoteProgress(bookId)
        } else if (saved.synced_at < saved.updated_at) {
            detectOfflineProgress(bookId, idx, meta.size)
        }
        // 打开阅读页即开始计时，避免把「呆在书架上的时间」算成阅读时长。
        lastActivityAt = Clock.System.now().toEpochMilliseconds()
    }

    /**
     * 本地有未同步进度时，向服务端确认一次：如果云端停得更靠前，
     * 说明用户在离线期间读得更远，弹窗让用户自己决定跳到哪里。
     *
     * 离线模式下不发请求（避免每次都等超时），等恢复在线后再判断。
     */
    private fun detectOfflineProgress(bookId: String, localChapter: Int, total: Int) {
        if (offline.value || localChapter <= 0) return
        scope.launch {
            val remote = runCatching { api.getProgress(bookId) }.getOrNull() ?: return@launch
            if (remote.chapterIndex >= localChapter) return@launch
            if ((screen.value as? Screen.Reader)?.bookId != bookId) return@launch
            offlinePrompt.value = OfflineProgressPrompt(
                bookId = bookId,
                localChapter = localChapter,
                remoteChapter = remote.chapterIndex,
                totalChapters = total,
            )
        }
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
        val started = Telemetry.now()
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
            recordSlowChapter(bookId, index, cached.title, Telemetry.now() - started, cached = true)
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
            recordSlowChapter(bookId, index, c.title, Telemetry.now() - started, cached = false)
            if (report) reportProgress(bookId, index, 0, percentOf(meta.size, index))
        } catch (e: Throwable) {
            reader.value = reader.value.copy(
                loading = false,
                message = "本章未缓存，且无法联网获取（${friendly(e)}）",
            )
        }
    }

    /** 解密 + 渲染 > 500ms 才上报，避免把正常章节灌进遥测。 */
    private fun recordSlowChapter(bookId: String, index: Int, title: String, cost: Long, cached: Boolean) {
        if (cost < 500) return
        val bookTitle = reader.value.book?.title ?: bookId
        Telemetry.record(
            kind = TelemetryKind.SLOW_CHAPTER,
            name = "$bookTitle·$title",
            durationMs = cost,
            context = mapOf(
                "book_id" to bookId,
                "chapter_index" to index.toString(),
                "cached" to cached.toString(),
            ),
        )
    }

    /** 预取当前章节之后的 3 章（离线阅读的核心动作）。 */
    fun prefetchAhead() {
        val bookId = (screen.value as? Screen.Reader)?.bookId ?: return
        if (!canPrefetch) {
            notice.value = "当前不是 WiFi，已跳过预取（避免消耗移动流量）"
            return
        }
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
            val started = Telemetry.now()
            try {
                prefetchInternal(bookId, need)
                refreshCachedIds()
                notice.value = "已缓存 ${need.size} 章（密文传输，本地解密）"
                Telemetry.record(
                    kind = TelemetryKind.PRELOAD_STATS,
                    name = "manual_prefetch",
                    durationMs = Telemetry.now() - started,
                    context = mapOf(
                        "book_id" to bookId,
                        "chapters" to need.size.toString(),
                        "trigger" to "manual",
                    ),
                )
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
        // 阅读时长：按「两次上报之间的活跃时长」估算，单次封顶 120 秒（防长时间挂后台失真）。
        val readSeconds = drainReadingSeconds()
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
                        readSeconds = readSeconds,
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

    // ---- 第八阶段：发现 / 足迹 / 收藏 / 更新 / 亮度 / 预读 ----

    fun loadDiscover() {
        scope.launch {
            try {
                discover.value = api.discover()
            } catch (e: Throwable) {
                error.value = friendly(e)
            }
        }
    }

    fun loadHistory() {
        scope.launch {
            try {
                history.value = api.history().items
            } catch (e: Throwable) {
                error.value = friendly(e)
            }
        }
    }

    /**
     * 切换收藏：先乐观更新本地库并刷新书架，再向服务端确认；
     * 失败则回滚本地并提示。全程只动元数据与 is_favorite，不触碰正文。
     */
    fun toggleFavorite(bookId: String) {
        val next = !store.isFavorite(bookId)
        store.setFavorite(bookId, next)
        books.value = store.listBooks()
        scope.launch {
            try {
                val confirmed = api.setFavorite(bookId, next)
                if (confirmed != next) {
                    store.setFavorite(bookId, confirmed)
                    books.value = store.listBooks()
                }
            } catch (e: Throwable) {
                store.setFavorite(bookId, !next)
                books.value = store.listBooks()
                error.value = "收藏失败：${friendly(e)}"
            }
        }
    }

    /** 调节系统亮度（由平台层实现；commonMain 不引用 Android Window）。 */
    fun setScreenBrightness(value: Float) {
        onBrightnessChange?.invoke(value.coerceIn(0.05f, 1f))
    }

    /**
     * 章节预读：当前进度超过 80% 时，静默把下一章密文拉回并解密写入 SQLCipher。
     * 失败一律静默（弱网下不应打扰阅读）；已缓存则直接返回。
     */
    fun prefetchNextChapter() {
        val bookId = (screen.value as? Screen.Reader)?.bookId ?: return
        // 静默预读只在 WiFi 下进行：移动网络下直接放弃，不打扰用户。
        if (!canPrefetch) return
        val r = reader.value
        val next = r.index + 1
        if (next >= r.chapters.size) return
        if (r.chapters.getOrNull(next)?.cached == true) return
        scope.launch {
            val started = Telemetry.now()
            runCatching { prefetchInternal(bookId, listOf(next)) }.onSuccess {
                Telemetry.record(
                    kind = TelemetryKind.PRELOAD_STATS,
                    name = "auto_prefetch",
                    durationMs = Telemetry.now() - started,
                    context = mapOf(
                        "book_id" to bookId,
                        "chapter_index" to next.toString(),
                        "trigger" to "scroll80",
                    ),
                )
            }
        }
    }

    /** 检查更新：拉取版本与说明，比较版本号。 */
    fun checkUpdate() {
        val u = update.value
        if (u.checking || u.downloading) return
        update.value = u.copy(checking = true, message = null, readyToInstall = null)
        scope.launch {
            try {
                val info = api.appVersion()
                val has = UpdatePlanner.isNewer(info.version, APP_VERSION)
                update.value = update.value.copy(
                    checking = false, info = info, hasUpdate = has,
                    message = if (has) null else "已是最新版本 ${info.version}",
                )
            } catch (e: Throwable) {
                update.value = update.value.copy(checking = false, message = "检查失败：${friendly(e)}")
            }
        }
    }

    /**
     * 下载更新：先取服务端哈希（缺失则不下载），再交由平台层测速下载并校验，
     * 校验通过才唤起安装器。绝不安装未校验的包。
     */
    fun downloadUpdate() {
        val u = update.value
        if (u.downloading) return
        val updater = appUpdater
        if (updater == null) {
            update.value = u.copy(message = "当前平台不支持应用内更新")
            return
        }
        val info = u.info ?: return
        update.value = u.copy(downloading = true, progress = 0f, message = null)
        scope.launch {
            try {
                val hash = api.latestHash()?.sha256
                if (hash.isNullOrBlank()) {
                    update.value = update.value.copy(
                        downloading = false, message = "服务端未提供校验值，已取消下载",
                    )
                    return@launch
                }
                val path = updater.downloadAndVerify(info.mirrors, hash) { p ->
                    update.value = update.value.copy(progress = p)
                }
                update.value = update.value.copy(
                    downloading = false, progress = 1f,
                    readyToInstall = path, message = "校验通过，正在唤起安装器",
                )
                runCatching { updater.install(path) }.onFailure {
                    update.value = update.value.copy(message = "无法唤起安装器：${it.message}")
                }
            } catch (e: Throwable) {
                update.value = update.value.copy(downloading = false, message = "下载失败：${friendly(e)}")
            }
        }
    }

    // ---- 搜索 ----

    fun search(query: String) {
        scope.launch {
            val started = Telemetry.now()
            val hits = store.search(query)
            searchResults.value = hits
            Telemetry.record(
                kind = TelemetryKind.SEARCH_STATS,
                name = "local_search",
                durationMs = Telemetry.now() - started,
                context = mapOf("query_len" to query.length.toString(), "hits" to hits.size.toString()),
            )
        }
    }

    // ---- 第九阶段：网络与遥测 ----

    /**
     * 网络切换回调（平台层调用）。
     *
     * 语义：
     *  - 断网/移动网络 → 立即停掉预读与同步，横幅提示；
     *  - 回到 WiFi → 恢复并顺便触发一次遥测上传（WorkManager 仍受 UNMETERED 约束）。
     */
    fun onNetworkChanged(status: NetworkStatus) {
        val prev = network.value
        if (prev == status) return
        network.value = status
        notice.value = when (status) {
            NetworkStatus.Online ->
                if (prev == NetworkStatus.Offline) "网络已恢复，预读与同步继续" else null
            NetworkStatus.Metered -> "已切到移动网络：预读与遥测上传已暂停（省流量）"
            NetworkStatus.Offline -> "网络已断开：预读与同步已暂停"
        }
        if (status == NetworkStatus.Online) {
            Telemetry.requestUpload()
        }
    }

    fun refreshTelemetryStats() {
        val c = telemetryControl ?: return
        runCatching { telemetryStats.value = c.stats() }
    }

    fun clearTelemetry() {
        val c = telemetryControl ?: return
        runCatching { c.clear() }
        refreshTelemetryStats()
        notice.value = "本地遥测已清空"
    }

    /** 「立即上传」：走 dataSync 前台服务，仅 WiFi 下真正传输。 */
    fun uploadTelemetryNow() {
        val c = telemetryControl ?: return
        if (!canPrefetch) {
            notice.value = "当前不是 WiFi，已跳过上传（遥测只在 WiFi 下传输）"
            return
        }
        runCatching { c.uploadNow() }
        notice.value = "已加入上传队列"
    }

    // ---- 第九阶段（本期）：离线模式 ----

    /**
     * 离线模式下的书架数据源。
     * 在线时原样返回；离线时只保留「至少有一章缓存」的书，避免点进去一片空白。
     */
    fun visibleBooks(all: List<Books>): List<Books> =
        if (!offline.value) all else all.filter { it.id in cachedIds.value }

    /** 手动重试：清空熔断计数并同步一次，让横幅能自己消失。 */
    fun retryConnection() {
        NetworkCircuit.reset()
        notice.value = "正在重试连接…"
        sync()
    }

    /** 刷新本地缓存书籍集合（同步/预读/删书后调用）。 */
    /**
     * 书架多选「移除本地缓存」。
     *
     * 只删本地副本（元数据 + 已缓存章节），不动服务端书籍——私人站的书籍由控制台
     * 管理，客户端不应有删除服务端数据的权限。下次同步会重新拉回元数据，
     * 但正文需要重新缓存，语义与 iOS 的「移除下载」一致。
     */
    fun removeLocalBooks(ids: Collection<String>) {
        if (ids.isEmpty()) return
        var n = 0
        for (id in ids) {
            runCatching { store.deleteBook(id) }.onSuccess { n++ }
        }
        books.value = store.listBooks()
        refreshCachedIds()
        notice.value = "已移除 $n 本书的本地缓存"
    }

    fun refreshCachedIds() {
        cachedIds.value = runCatching { store.cachedBookIds() }.getOrDefault(emptySet())
    }

    /** 加载阅读统计；失败时静默（统计不是主流程）。 */
    fun loadReadingStats() {
        scope.launch {
            runCatching { api.readingStats() }
                .onSuccess { readingStats.value = it }
                .onFailure { if (!offline.value) error.value = friendly(it) }
        }
    }

    /** 用户选择「跳到离线进度」或「留在云端进度」。 */
    fun resolveOfflinePrompt(useLocal: Boolean) {
        val p = offlinePrompt.value ?: return
        offlinePrompt.value = null
        if (!useLocal) {
            // 放弃本地进度：把本地记录回退到云端位置，避免下次又提示。
            store.saveProgress(p.bookId, p.remoteChapter, 0, percentOf(reader.value.chapters.size, p.remoteChapter))
            scope.launch { loadChapter(p.bookId, p.remoteChapter, report = false) }
            return
        }
        notice.value = "已回到离线进度：第 ${p.localChapter + 1} 章"
        scope.launch {
            prefetchInternal(p.bookId, listOf(p.localChapter))
            loadChapter(p.bookId, p.localChapter, report = false)
        }
    }

    // ---- 第十阶段：局域网发现 / 动态根地址 / 短码配对 ----

    /** 当前生效的根地址（供设置页展示）。 */
    fun currentEndpoint(): String = com.novelstack.reader.net.ApiEndpointManager.activeBaseUrl()

    /** 当前是否走内网节点（供设置页刷新）。 */
    fun refreshEndpointState() {
        usingLocalEndpoint.value = com.novelstack.reader.net.ApiEndpointManager.isLocalActive()
    }

    /**
     * 手动修改服务器地址（公网主地址）。
     * 不清空 token：同一服务器的地址变更不应强制重新配对。
     */
    fun setPrimaryServer(server: String) {
        val mgr = com.novelstack.reader.net.ApiEndpointManager
        val normalized = com.novelstack.reader.net.NovelApi.normalizeServer(server)
        if (normalized.isBlank()) {
            endpointMessage.value = "服务器地址不能为空"
            return
        }
        mgr.configure(normalized, mgr.localUrl(), mgr.deviceToken())
        store.kvPut("server_url", normalized)
        serverUrl.value = normalized
        api.configure(normalized, mgr.deviceToken().ifBlank { null })
        refreshEndpointState()
        endpointMessage.value = "主服务器已设为 $normalized"
    }

    /** 设置/清除内网地址（双节点降级的探测目标）。 */
    fun setLocalServer(local: String) {
        val mgr = com.novelstack.reader.net.ApiEndpointManager
        val normalized = if (local.isBlank()) "" else com.novelstack.reader.net.NovelApi.normalizeServer(local)
        mgr.configure(mgr.primaryUrl(), normalized, mgr.deviceToken())
        refreshEndpointState()
        endpointMessage.value = if (normalized.isBlank()) "已关闭内网节点" else "内网节点已设为 $normalized"
    }

    /** 从剪贴板文本解析并一键切换服务器（JSON 或 base64(JSON)）。 */
    fun importEndpoint(raw: String) {
        val mgr = com.novelstack.reader.net.ApiEndpointManager
        val imp = mgr.parseImport(raw)
        if (imp == null) {
            endpointMessage.value = "无法解析：请粘贴包含 url 与 token 的 JSON 或 Base64 文本"
            return
        }
        val primary = com.novelstack.reader.net.NovelApi.normalizeServer(imp.primary())
        mgr.configure(primary, imp.localUrl, imp.resolvedToken())
        api.configure(primary, imp.resolvedToken().ifBlank { null })
        store.kvPut("server_url", primary)
        if (imp.resolvedToken().isNotBlank()) store.kvPut("device_token", imp.resolvedToken())
        serverUrl.value = primary
        refreshEndpointState()
        endpointMessage.value = "已切换服务器：$primary${imp.name.takeIf { it.isNotBlank() }?.let { " ($it)" } ?: ""}"
        // 切换服务器后重新同步一次；凭证失效会由 sync 内部处理。
        scope.launch {
            runCatching { syncInternal() }.onFailure {
                endpointMessage.value = "已切换服务器，但首次同步失败：${friendly(it)}"
            }
        }
    }

    /** 导出当前配置文本（base64 JSON），供用户复制到其它设备。 */
    fun exportEndpoint(): String = com.novelstack.reader.net.ApiEndpointManager.exportConfig()

    /** 开始/停止局域网发现。 */
    fun startLanDiscovery() {
        val c = lanDiscovery ?: run {
            endpointMessage.value = "当前平台不支持局域网发现"
            return
        }
        c.start { services ->
            lanServices.value = services
        }
    }

    fun stopLanDiscovery() {
        lanDiscovery?.stop()
    }

    /** 用发现的某台服务器 + 6 位短码完成配对。 */
    fun pairLanService(service: com.novelstack.reader.model.LanService, code: String, deviceName: String) {
        if (busy.value) return
        val digits = code.trim()
        if (digits.length != 6 || digits.any { it !in '0'..'9' }) {
            endpointMessage.value = "配对码必须是 6 位数字"
            return
        }
        error.value = null
        busy.value = true
        scope.launch {
            try {
                keyStore.ensureKeys()
                val auth = api.lanPair(
                    service.endpoint,
                    com.novelstack.reader.model.LanPairRequest(
                        pairingCode = digits,
                        deviceName = deviceName.ifBlank { "Android 阅读器" },
                        publicKey = keyStore.publicKeyPem(),
                    ),
                )
                // 配对成功：把发现到的内网地址设为 localUrl，公网地址保留或置空。
                val mgr = com.novelstack.reader.net.ApiEndpointManager
                val primary = mgr.primaryUrl()
                mgr.configure(primary, service.endpoint, auth.token)
                api.configure(primary.ifBlank { service.endpoint }, auth.token)
                store.kvPut("server_url", primary.ifBlank { service.endpoint })
                store.kvPut("device_token", auth.token)
                store.kvPut("device_id", auth.device.id)
                store.kvPut("device_name", auth.device.name)
                serverUrl.value = primary.ifBlank { service.endpoint }
                deviceId.value = auth.device.id
                Telemetry.updateDeviceId(auth.device.id)
                Telemetry.requestUpload()
                stopLanDiscovery()
                refreshEndpointState()
                endpointMessage.value = "局域网配对成功：${service.name}"
                syncInternal()
                screen.value = Screen.Library
            } catch (e: Throwable) {
                endpointMessage.value = "配对失败：${friendly(e)}"
            } finally {
                busy.value = false
            }
        }
    }

    /** 唤起二维码扫描，扫到 LAN 载荷后直接配对。 */
    fun scanQrToPair(deviceName: String) {
        val scanner = onScanQr
        if (scanner == null) {
            endpointMessage.value = "当前平台不支持二维码扫描，请手动输入 6 位码"
            return
        }
        scanner { raw ->
            val payload = com.novelstack.reader.net.ApiEndpointManager.parseLanPairing(raw)
            if (payload == null) {
                endpointMessage.value = "二维码不是有效的配对码（应为控制台生成的 JSON）"
            } else {
                pairFromPayload(raw, deviceName)
            }
        }
    }

    /** 用剪贴板/二维码里的 LAN 载荷完成配对。 */
    fun pairFromPayload(raw: String, deviceName: String) {
        val mgr = com.novelstack.reader.net.ApiEndpointManager
        val payload = mgr.parseLanPairing(raw)
        if (payload == null) {
            endpointMessage.value = "无法解析配对二维码：请扫描控制台生成的二维码"
            return
        }
        val service = com.novelstack.reader.model.LanService(
            name = payload.name.ifBlank { "NovelStack" },
            host = "",
            port = 0,
            endpoint = payload.endpoint,
        )
        pairLanService(service, payload.code, deviceName)
    }

    // ---- 终端防御阶段：应用锁 / 防截屏 / 文本操作 / 触感 ----

    /**
     * 生命周期：进入后台时记录时间，并立即上锁。
     *
     * 立即上锁是为了最近任务缩略图与回前台第一帧不暴露内容；
     * 离开不足 30 秒时 [onEnterForeground] 会自动放行，不打扰用户。
     * 由 MainActivity.onPause 调用（平台侧保证）。
     */
    fun onEnterBackground(nowMs: Long = currentMs()) {
        backgroundSinceMs = nowMs
        stopForegroundSync()
        if (settings.value.appLockEnabled && appLock.isAvailable() && appLock.isEnrolled()) {
            locked.value = true
            lockOnBackground = true
        }
    }

    /**
     * 回到前台：后台超过 30 秒则保持锁定并要求验证；不足 30 秒且是后台触发的锁定则自动放行。
     * 冷启动时 backgroundSinceMs=0，不重复触发（init 里已处理）。
     */
    fun onEnterForeground(nowMs: Long = currentMs()) {
        // 回前台顺带刷新电量快照（广播可能在此期间丢失）。
        power.value = powerMonitor.current()
        val bg = backgroundSinceMs
        backgroundSinceMs = 0L
        val wasBackgroundLock = lockOnBackground
        lockOnBackground = false
        if (bg <= 0L) return
        if (!settings.value.appLockEnabled || !appLock.isAvailable() || !appLock.isEnrolled()) {
            if (wasBackgroundLock) locked.value = false
            return
        }
        locked.value = nowMs - bg >= 30_000L
        startForegroundSync()
    }

    /**
     * 前台增量同步：每 5 分钟拉一次进度与书架变更。
     *
     * 第十二阶段明确放弃长连接（WebSocket）：移动网络下心跳既费电又不可靠，
     * 而私人站的进度冲突概率极低。翻页仍走「条件触发」的即时上报，
     * 定时拉取只负责把其它端的变化带回来。
     */
    fun startForegroundSync() {
        if (syncLoopJob?.isActive == true) return
        if (!api.hasToken()) return
        syncLoopJob = scope.launch {
            while (isActive) {
                delay(FOREGROUND_SYNC_INTERVAL_MS)
                if (!api.hasToken()) return@launch
                if (network.value != NetworkStatus.Online) continue
                runCatching { syncInternal() }
            }
        }
    }

    fun stopForegroundSync() {
        syncLoopJob?.cancel()
        syncLoopJob = null
    }

    /** 发起生物识别；成功则解锁。失败由 UI 展示模糊遮罩与重试提示。 */
    fun unlockWithBiometric(onError: (Int, String) -> Unit) {
        appLock.authenticate(
            onSuccess = { locked.value = false },
            onError = onError,
        )
    }

    /** 设备当前是否具备可用的应用锁条件（供设置页决定开关是否可开）。 */
    fun appLockCapable(): Boolean = appLock.isAvailable() && appLock.isEnrolled()

    /**
     * 根据当前页面更新 FLAG_SECURE。只有开关打开且页面属于敏感页（阅读/设置）时才加。
     */
    fun updateSecureFlag(screen: Screen) {
        val sensitive = screen is Screen.Reader || screen is Screen.Settings || screen is Screen.ServerSettings
        val want = settings.value.secureScreen && sensitive
        if (secureNow.value != want) {
            secureNow.value = want
            screenGuard.setSecure(want)
        }
    }

    /** 设置变更后重新应用防截屏（开关被关掉时立即清除 FLAG_SECURE）。 */
    fun reapplySecureFlag(screen: Screen) {
        secureNow.value = false
        updateSecureFlag(screen)
    }

    /** 文本操作：复制。 */
    fun copyText(text: String) {
        if (text.isBlank()) return
        textActions.copy(text)
        notice.value = "已复制"
    }

    /** 文本操作：网页翻译（ACTION_PROCESS_TEXT）。无可用应用时给出提示。 */
    fun processText(text: String) {
        if (text.isBlank()) return
        if (!textActions.processText(text)) notice.value = "未找到可用的翻译/词典应用"
    }

    /** 划词本地搜索的预填词（SearchScreen 进入时读取并清空）。 */
    val searchPrefill = MutableStateFlow<String?>(null)

    /**
     * 打开 Web 控制台（WebView 免密）。
     *
     * 客户端只有 device token，浏览器 cookie 是 HttpOnly，无法直接注入；
     * 因此先换一张 60 秒一次性票据，再由 WebView 打开登录页完成兑换。
     */
    fun requestWebConsole(onReady: (String) -> Unit) {
        scope.launch {
            try {
                val g = api.webGrant()
                if (g.url.isBlank()) {
                    notice.value = "服务端未返回控制台地址"
                } else {
                    onReady(g.url)
                }
            } catch (e: Throwable) {
                notice.value = if (e is ApiException && e.forbidden) {
                    "该服务端未启用 Web 控制台"
                } else {
                    "无法打开控制台：${friendly(e)}"
                }
            }
        }
    }

    /** 前台定时同步是否在跑（设置页展示用）。 */
    fun foregroundSyncActive(): Boolean = syncLoopJob?.isActive == true

    /** 文本操作：本地搜索——填充查询词并跳到搜索页。 */
    fun searchLocalText(text: String) {
        val q = text.trim().take(100)
        if (q.isEmpty()) return
        searchPrefill.value = q
        screen.value = Screen.Search
    }

    /** 翻页触感：由 HapticPolicy 节流 + 低电量抑制，返回是否应振动。 */
    fun shouldHaptic(nowMs: Long = currentMs()): Boolean {
        val ok = HapticPolicy.shouldVibrate(lastHapticAtMs, nowMs, settings.value.hapticsEnabled, power.value.isLow)
        if (ok) lastHapticAtMs = nowMs
        return ok
    }

    private fun currentMs(): Long = kotlinx.datetime.Clock.System.now().toEpochMilliseconds()

    // ---- 系统保活 ----

    fun keepAliveInfo(): KeepAliveInfo? = keepAlive?.let {
        KeepAliveInfo(
            deviceLabel = it.deviceLabel(),
            batteryIgnored = it.batteryOptimizationIgnored(),
            hint = it.vendorHint(),
        )
    }

    fun requestIgnoreBatteryOptimizations() {
        keepAlive?.requestIgnoreBatteryOptimizations()
    }

    fun openAutostartSettings() {
        val opened = keepAlive?.openAutostartSettings() ?: return
        notice.value = if (opened) null else "未找到厂商自启动页，已打开应用详情，请手动允许自启动"
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
            com.novelstack.reader.net.ApiEndpointManager.clear()
            api.configure("", null)
            books.value = emptyList()
            reader.value = ReaderUiState()
            searchResults.value = emptyList()
            discover.value = null
            history.value = emptyList()
            favoritesOnly.value = false
            update.value = UpdateUiState()
            readingStats.value = null
            cachedIds.value = emptySet()
            offlinePrompt.value = null
            lanServices.value = emptyList()
            usingLocalEndpoint.value = false
            endpointMessage.value = null
            NetworkCircuit.clear()
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
        else -> if (offline.value) {
            "网络不稳定，已进入离线模式：只显示已缓存内容"
        } else {
            e.message ?: "未知错误"
        }
    }

    companion object {
        /**
         * 与构建注入的 versionName 同源（CI 由 tag 推导，见 shared/build.gradle.kts 的 APP_VERSION），
         * 用于「检查更新」的版本比较与设置页展示。
         */
        val APP_VERSION: String = appVersionName()

        /** 前台增量同步间隔：5 分钟。 */
        const val FOREGROUND_SYNC_INTERVAL_MS = 5 * 60 * 1000L
    }
}
