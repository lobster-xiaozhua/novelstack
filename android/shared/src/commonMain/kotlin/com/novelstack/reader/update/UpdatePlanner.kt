package com.novelstack.reader.update

/**
 * 更新相关的纯逻辑：镜像选择与哈希校验。
 * 与平台无关，便于单测；实际网络测速/下载在平台层实现。
 */
object UpdatePlanner {

    /**
     * 从（镜像 URL, 测速耗时毫秒）中选最快的一个。
     * - 耗时 <= 0 视为该镜像失败，忽略；
     * - 全部失败返回 null；
     * - 并列时取先出现者（保持稳定）。
     */
    fun pickFastest(probes: List<Pair<String, Long>>): String? {
        var best: String? = null
        var bestMs = Long.MAX_VALUE
        for ((url, ms) in probes) {
            if (ms <= 0L) continue
            if (ms < bestMs) {
                bestMs = ms
                best = url
            }
        }
        return best
    }

    /** 校验下载内容的 SHA-256（十六进制，大小写不敏感）。 */
    fun verify(bytes: ByteArray, expectedSha256: String): Boolean =
        Sha256.matches(bytes, expectedSha256)

    /** 判断远端版本是否比当前版本新（点分数字比较；无法解析时按「不相同即提示」）。 */
    fun isNewer(remote: String, current: String): Boolean {
        if (remote.isBlank()) return false
        if (remote == current) return false
        val r = parseVersion(remote) ?: return true
        val c = parseVersion(current) ?: return true
        val n = maxOf(r.size, c.size)
        for (i in 0 until n) {
            val a = r.getOrElse(i) { 0 }
            val b = c.getOrElse(i) { 0 }
            if (a != b) return a > b
        }
        return false
    }

    private fun parseVersion(v: String): List<Int>? {
        val core = v.trim().removePrefix("v").substringBefore('-')
        if (core.isEmpty()) return null
        val parts = core.split('.')
        val out = ArrayList<Int>(parts.size)
        for (p in parts) {
            val n = p.toIntOrNull() ?: return null
            out.add(n)
        }
        return out
    }
}

/** 平台无关的更新器接口：下载校验 + 安装。 */
interface AppUpdater {
    /**
     * 并发对所有镜像发起测速（前 100KB），选最快者下载完整 APK，
     * 计算 SHA-256 并与 [expectedSha256] 比对。成功返回本地文件路径。
     *
     * 任何失败（全部镜像不可达 / 哈希不符 / 写盘失败）都应抛异常，
     * 由上层提示用户，绝不能返回未校验的路径。
     */
    suspend fun downloadAndVerify(
        mirrors: List<String>,
        expectedSha256: String,
        onProgress: (Float) -> Unit,
    ): String

    /** 通过 FileProvider 唤起系统安装器（Android 7.0+）。 */
    fun install(apkPath: String)
}
