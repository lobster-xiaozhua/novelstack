package com.novelstack.reader.app

import com.novelstack.reader.model.LanService

/**
 * 局域网服务发现控制器（commonMain 抽象）。
 *
 * Android 用 NsdManager（mDNS _novelstack._tcp）+ UDP 广播兜底实现；
 * 其它平台可返回空列表。发现只返回地址信息，不含任何凭证或数据。
 */
interface LanDiscoveryController {
    /** 开始发现；[onResult] 在后台线程回调，实现方需切回主线程由调用方处理。 */
    fun start(onResult: (List<LanService>) -> Unit)
    fun stop()
}
