package com.novelstack.reader.net

import io.ktor.client.network.sockets.ConnectTimeoutException
import io.ktor.client.network.sockets.SocketTimeoutException
import io.ktor.client.plugins.HttpRequestTimeoutException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/**
 * 离线熔断器。
 *
 * 规则（验收 C2）：
 *  - 只统计「超时」类失败（连接/请求/读写超时），4xx/5xx 不算——那说明服务器活着；
 *  - 连续 3 次超时后进入离线模式；
 *  - 任意一次成功立即恢复在线；
 *  - 进入离线模式后：顶部横幅提示、书架只显示有缓存的书籍、未缓存章节置灰、
 *    遥测继续在本地堆积（WorkManager 仍受 UNMETENRED 约束），不弹错误框。
 *
 * 实现刻意做成进程级单例：HttpClient 与 AppState 需要看到同一份状态，
 * 而 KMP 里没有全局 Application 对象可用。
 */
object NetworkCircuit {
    const val THRESHOLD = 3

    private var consecutiveTimeouts: Int = 0

    private val _offline = MutableStateFlow(false)
    val offline: StateFlow<Boolean> = _offline

    /** 记录一次成功：立即解除离线（服务器显然可达）。 */
    fun onSuccess() {
        consecutiveTimeouts = 0
        if (_offline.value) _offline.value = false
    }

    /** 记录一次失败：只有超时才计数。 */
    fun onFailure(t: Throwable) {
        if (!isTimeout(t)) return
        consecutiveTimeouts++
        if (consecutiveTimeouts >= THRESHOLD && !_offline.value) {
            _offline.value = true
        }
    }

    /** 用户手动「重试」时调用，重新开始计数。 */
    fun reset() {
        consecutiveTimeouts = 0
        _offline.value = false
    }

    /** 退出登录时清空，避免把离线状态带到下一个会话。 */
    fun clear() {
        consecutiveTimeouts = 0
        _offline.value = false
    }
}

/**
 * 判断异常链上是否存在超时。
 *
 * Ktor 不同引擎可能抛不同子类，OkHttp 还可能把底层 SocketTimeoutException
 * 包一层；因此沿 cause 链递归检查。
 */
fun isTimeout(t: Throwable?): Boolean {
    var cur = t
    var depth = 0
    while (cur != null && depth < 8) {
        when (cur) {
            is HttpRequestTimeoutException, is ConnectTimeoutException, is SocketTimeoutException -> return true
        }
        // 各平台底层异常（如 java.net.SocketTimeoutException）类型不同，
        // commonMain 不能直接引用，这里按类名兜底判断。
        val name = cur::class.simpleName ?: ""
        if (name.contains("Timeout", ignoreCase = true)) return true
        cur = cur.cause
        depth++
    }
    return false
}
