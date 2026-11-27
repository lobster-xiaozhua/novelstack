package com.novelstack.reader.platform

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import com.novelstack.reader.app.LanDiscoveryController
import com.novelstack.reader.model.LanService
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import org.json.JSONObject
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.SocketTimeoutException

/**
 * Android 局域网发现：NsdManager（mDNS）+ UDP 广播兜底。
 *
 * 发现的都是地址信息（host/port），绝不返回书籍数据或凭证。
 * UDP 广播用自研 {"type":"probe"} -> {"type":"answer",...} 协议，
 * 兼容国内 ROM 屏蔽 mDNS 组播的情况。
 */
class AndroidLanDiscovery(
    private val context: Context,
    private val scope: CoroutineScope,
) : LanDiscoveryController {

    private val nsd = context.getSystemService(Context.NSD_SERVICE) as? NsdManager
    private var listener: NsdManager.DiscoveryListener? = null
    private var udpJob: Job? = null
    private val found = LinkedHashMap<String, LanService>()

    @Volatile
    private var callback: ((List<LanService>) -> Unit)? = null

    override fun start(onResult: (List<LanService>) -> Unit) {
        callback = onResult
        found.clear()
        startNsd()
        startUdpFallback()
    }

    override fun stop() {
        callback = null
        stopNsd()
        udpJob?.cancel()
        udpJob = null
    }

    // ---- mDNS ----

    private fun startNsd() {
        val mgr = nsd ?: return
        val l = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(serviceType: String) {}
            override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {}
            override fun onDiscoveryStopped(serviceType: String) {}
            override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) {}

            override fun onServiceFound(serviceInfo: NsdServiceInfo) {
                if (serviceInfo.serviceType != SERVICE_TYPE) return
                val resolve = object : NsdManager.ResolveListener {
                    override fun onResolveFailed(info: NsdServiceInfo?, errorCode: Int) {}
                    override fun onServiceResolved(info: NsdServiceInfo?) {
                        info ?: return
                        val host = info.host?.hostAddress ?: return
                        val path = runCatching {
                            info.attributes["path"]?.let { String(it) }
                        }.getOrNull() ?: "/"
                        val key = "mdns:$host:${info.port}"
                        synchronized(found) {
                            found[key] = LanService(
                                name = info.serviceName ?: "NovelStack",
                                host = host,
                                port = info.port,
                                endpoint = "http://$host:${info.port}" + path.trimEnd('/'),
                            )
                        }
                        emit()
                    }
                }
                runCatching { mgr.resolveService(serviceInfo, resolve) }
            }

            override fun onServiceLost(serviceInfo: NsdServiceInfo) {
                val host = serviceInfo.host?.hostAddress ?: return
                synchronized(found) { found.remove("mdns:$host:${serviceInfo.port}") }
                emit()
            }
        }
        listener = l
        runCatching { mgr.discoverServices(SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, l) }
    }

    private fun stopNsd() {
        val mgr = nsd ?: return
        listener?.let { runCatching { mgr.stopServiceDiscovery(it) } }
        listener = null
    }

    // ---- UDP 广播兜底 ----

    private fun startUdpFallback() {
        udpJob?.cancel()
        udpJob = scope.launch(Dispatchers.IO) {
            repeat(3) { // 连发 3 轮，每轮 800ms，提高被局域网服务收到的概率
                sendProbeAndCollect()
                delay(900)
            }
        }
    }

    private fun sendProbeAndCollect() {
        val socket = runCatching { DatagramSocket().apply { soTimeout = 700 } }.getOrNull() ?: return
        try {
            val payload = "{\"type\":\"probe\"}".toByteArray(Charsets.UTF_8)
            val packet = DatagramPacket(payload, payload.size, broadcastAddress(), UDP_PORT)
            runCatching { socket.send(packet) }
            // 收集应答（最多 6 个 / 700ms）。
            val deadline = System.currentTimeMillis() + 700
            while (System.currentTimeMillis() < deadline) {
                val buf = ByteArray(2048)
                val reply = DatagramPacket(buf, buf.size)
                try {
                    socket.receive(reply)
                } catch (_: SocketTimeoutException) {
                    break
                } catch (_: Exception) {
                    break
                }
                val text = String(reply.data, 0, reply.length, Charsets.UTF_8)
                parseAnswer(text, reply.address.hostAddress ?: continue)?.let { svc ->
                    synchronized(found) { found["udp:${svc.host}:${svc.port}"] = svc }
                    emitToMain()
                }
            }
        } finally {
            socket.close()
        }
    }

    private fun parseAnswer(text: String, fallbackHost: String): LanService? = runCatching {
        val o = JSONObject(text)
        if (o.optString("type") != "answer") return@runCatching null
        val host = o.optString("host").ifBlank { fallbackHost }
        val port = o.optInt("port", 0)
        if (port <= 0) return@runCatching null
        val endpoint = o.optString("endpoint").ifBlank { "http://$host:$port" }
        LanService(name = o.optString("name").ifBlank { "NovelStack" }, host = host, port = port, endpoint = endpoint)
    }.getOrNull()

    private fun emit() {
        scope.launch(Dispatchers.Main) { callback?.invoke(snapshot()) }
    }

    private fun emitToMain() {
        scope.launch(Dispatchers.Main) { callback?.invoke(snapshot()) }
    }

    private fun snapshot(): List<LanService> =
        synchronized(found) { found.values.toList() }

    private fun broadcastAddress(): InetAddress {
        // 255.255.255.255 在多数 WiFi 下可达；部分 ROM 需显式子网广播，这里用受限广播兜底。
        return InetAddress.getByName("255.255.255.255")
    }

    private companion object {
        const val SERVICE_TYPE = "_novelstack._tcp."
        const val UDP_PORT = 5359
    }
}
