package com.novelstack.reader.platform

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.net.NetworkCapabilities
import com.novelstack.reader.app.NetworkStatus

/**
 * 网络状态监听。网络切换时回调给 AppState：
 *  - Offline：横幅提示，暂停预读/同步；
 *  - Metered：横幅提示「移动网络」，同样暂停大流量动作（预读）与遥测上传；
 *  - Online：恢复，并借机触发一次遥测上传（WiFi 约束由 WorkManager 兜底）。
 */
class NetworkMonitor(
    context: Context,
    private val onChange: (NetworkStatus) -> Unit,
) {
    private val cm = context.getSystemService(ConnectivityManager::class.java)
    private var registered = false

    private val callback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) = emit()
        override fun onLost(network: Network) = emit()
        override fun onCapabilitiesChanged(network: Network, caps: NetworkCapabilities) = emit()
    }

    fun start() {
        if (registered || cm == null) return
        runCatching {
            cm.registerDefaultNetworkCallback(callback)
            registered = true
        }
        emit()
    }

    fun stop() {
        if (!registered) return
        runCatching { cm?.unregisterNetworkCallback(callback) }
        registered = false
    }

    fun current(): NetworkStatus = status()

    private fun emit() {
        onChange(status())
    }

    private fun status(): NetworkStatus {
        val caps = cm?.getNetworkCapabilities(cm.activeNetwork) ?: return NetworkStatus.Offline
        if (!caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)) return NetworkStatus.Offline
        return if (caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)) {
            NetworkStatus.Online
        } else {
            NetworkStatus.Metered
        }
    }
}
