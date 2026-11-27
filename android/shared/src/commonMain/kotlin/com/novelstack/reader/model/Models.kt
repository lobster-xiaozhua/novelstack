package com.novelstack.reader.model

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * 与服务端 JSON 一一对应的 DTO。
 *
 * 注意：所有正文字段都是「密文」，客户端在设备内解密后才写入 SQLCipher。
 * 任何 DTO 都不得包含密钥明文（DEK 只以 wrapped_dek 形式出现，且只在内存中短暂存在）。
 */

@Serializable
data class DeviceAuth(
    val token: String,
    @SerialName("token_type") val tokenType: String = "Bearer",
    val device: DeviceInfo,
    @SerialName("server_time") val serverTime: String = "",
    @SerialName("expires_at") val expiresAt: String? = null,
)

@Serializable
data class DeviceInfo(
    val id: String,
    val name: String,
    @SerialName("token_prefix") val tokenPrefix: String = "",
    @SerialName("has_public_key") val hasPublicKey: Boolean = false,
    @SerialName("created_at") val createdAt: String = "",
    @SerialName("last_used_at") val lastUsedAt: String? = null,
    @SerialName("revoked_at") val revokedAt: String? = null,
    @SerialName("expires_at") val expiresAt: String? = null,
)

@Serializable
data class LoginRequest(
    val username: String = "",
    val password: String = "",
    @SerialName("pairing_code") val pairingCode: String = "",
    @SerialName("device_name") val deviceName: String = "",
    @SerialName("public_key") val publicKey: String = "",
)

@Serializable
data class ChapterRef(
    val index: Int,
    val title: String,
    val chars: Int = 0,
    val etag: String = "",
)

@Serializable
data class BookChange(
    val id: String,
    val title: String,
    val author: String = "",
    val tags: List<String> = emptyList(),
    val description: String = "",
    @SerialName("chapter_count") val chapterCount: Int = 0,
    @SerialName("size_bytes") val sizeBytes: Long = 0,
    val sha256: String = "",
    @SerialName("chunk_size") val chunkSize: Int = 0,
    @SerialName("kek_version") val kekVersion: Int = 1,
    @SerialName("created_at") val createdAt: String = "",
    @SerialName("sync_seq") val syncSeq: Long = 0,
    val chapters: List<ChapterRef> = emptyList(),
    @SerialName("wrapped_dek") val wrappedDek: String = "",
)

@Serializable
data class SyncManifest(
    val cursor: Long = 0,
    val since: Long = 0,
    val full: Boolean = false,
    @SerialName("reset_required") val resetRequired: Boolean = false,
    @SerialName("generated_at") val generatedAt: String = "",
    val count: Int = 0,
    val books: List<BookChange> = emptyList(),
    val deleted: List<String> = emptyList(),
)

@Serializable
data class PrefetchRequest(val indexes: List<Int>)

@Serializable
data class PrefetchChapter(
    val index: Int,
    val title: String = "",
    val etag: String = "",
    val bytes: Int = 0,
    val ciphertext: String = "",
)

@Serializable
data class PrefetchResponse(
    @SerialName("book_id") val bookId: String,
    val count: Int = 0,
    val bytes: Long = 0,
    val truncated: Boolean = false,
    @SerialName("wrapped_dek") val wrappedDek: String = "",
    val chapters: List<PrefetchChapter> = emptyList(),
)

@Serializable
data class ProgressDto(
    @SerialName("book_id") val bookId: String = "",
    val title: String = "",
    @SerialName("chapter_index") val chapterIndex: Int = 0,
    @SerialName("char_offset") val charOffset: Int = 0,
    val percent: Double = 0.0,
    @SerialName("device_id") val deviceId: String = "",
    @SerialName("updated_at") val updatedAt: String = "",
)

@Serializable
data class ProgressRequest(
    @SerialName("chapter_index") val chapterIndex: Int,
    @SerialName("char_offset") val charOffset: Int,
    val percent: Double,
    @SerialName("device_id") val deviceId: String = "",
)

@Serializable
data class ProgressResponse(
    val ok: Boolean = false,
    val progress: ProgressDto? = null,
)

@Serializable
data class AudioSegment(
    val key: String,
    val index: Int = 0,
    @SerialName("file_bytes") val fileBytes: Long = 0,
    @SerialName("ct_offset") val ctOffset: Int = 0,
    @SerialName("ct_len") val ctLen: Int = 0,
    @SerialName("audio_nonce") val audioNonce: String = "",
    @SerialName("wrapped_dek") val wrappedDek: String = "",
)

@Serializable
data class AudioManifest(
    @SerialName("book_id") val bookId: String = "",
    @SerialName("chapter_index") val chapterIndex: Int = 0,
    val count: Int = 0,
    @SerialName("total_bytes") val totalBytes: Long = 0,
    val ready: Boolean = false,
    val missing: Int = 0,
    val segments: List<AudioSegment> = emptyList(),
)

@Serializable
data class PrepareResponse(val ok: Boolean = false, val segments: Int = 0)

@Serializable
data class ApiError(val error: String = "")

/** 服务端明确返回的 HTTP 错误（保留状态码，便于区分 401/403/429）。 */
class ApiException(val status: Int, message: String) : Exception(message) {
    val unauthorized: Boolean get() = status == 401
    val forbidden: Boolean get() = status == 403
    val rateLimited: Boolean get() = status == 429
}

/** 阅读进度（本地模型，用于上报与服务端回读）。 */
data class ReadingProgress(
    val chapterIndex: Int,
    val charOffset: Int,
    val percent: Float,
)

// ---- 第八阶段：发现页 / 足迹 / 收藏 / 版本 ----

/** 收藏开关请求体。 */
@Serializable
data class FavoriteRequest(@SerialName("is_favorite") val isFavorite: Boolean)

/** 收藏接口响应（回显最终状态）。 */
@Serializable
data class FavoriteResponse(
    val ok: Boolean = false,
    @SerialName("book_id") val bookId: String = "",
    @SerialName("is_favorite") val isFavorite: Boolean = false,
)

/** 足迹条目：只有元数据与进度，绝无正文。 */
@Serializable
data class HistoryItem(
    @SerialName("book_id") val bookId: String = "",
    val title: String = "",
    val cover: String = "",
    @SerialName("chapter_index") val chapterIndex: Int = 0,
    @SerialName("chapter_title") val chapterTitle: String = "",
    val percent: Double = 0.0,
    @SerialName("updated_at") val updatedAt: String = "",
)

@Serializable
data class HistoryResponse(val items: List<HistoryItem> = emptyList())

/** 发现页卡片（元数据）。 */
@Serializable
data class DiscoverBook(
    val id: String = "",
    val title: String = "",
    val author: String = "",
    val tags: List<String> = emptyList(),
    @SerialName("chapter_count") val chapterCount: Int = 0,
    @SerialName("created_at") val createdAt: String = "",
    @SerialName("is_favorite") val isFavorite: Boolean = false,
)

@Serializable
data class TagCount(val name: String = "", val count: Int = 0)

@Serializable
data class DiscoverResponse(
    val recent: List<DiscoverBook> = emptyList(),
    val tags: List<TagCount> = emptyList(),
    val random: List<DiscoverBook> = emptyList(),
)

/** App 版本信息（含镜像列表，由服务端环境变量注入）。 */
@Serializable
data class AppVersion(
    val version: String = "",
    val changelog: String = "",
    val mirrors: List<String> = emptyList(),
)

/** 最新 APK 的 SHA-256。 */
@Serializable
data class LatestHash(
    val version: String = "",
    val sha256: String = "",
)
