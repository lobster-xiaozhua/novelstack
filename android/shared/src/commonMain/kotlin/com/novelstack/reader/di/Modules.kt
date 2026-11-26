package com.novelstack.reader.di

import com.novelstack.reader.net.NovelApi
import kotlinx.serialization.json.Json
import org.koin.dsl.module

/** 跨平台单例（与 IReader 的 Koin 结构一致）。 */
fun appModule() = module {
    single {
        Json {
            ignoreUnknownKeys = true
            explicitNulls = false
            encodeDefaults = true
        }
    }
    single { NovelApi(get(), get()) }
}
