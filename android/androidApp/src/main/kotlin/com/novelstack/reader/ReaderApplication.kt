package com.novelstack.reader

import android.app.Application
import com.novelstack.reader.app.AppState
import com.novelstack.reader.data.createReaderStore
import com.novelstack.reader.di.appModule
import com.novelstack.reader.di.platformModule
import com.novelstack.reader.net.NovelApi
import com.novelstack.reader.platform.AudioSinkFactory
import com.novelstack.reader.platform.DeviceKeyStore
import com.novelstack.reader.platform.LocalDataWiper
import kotlinx.coroutines.CoroutineScope
import kotlinx.serialization.json.Json
import org.koin.android.ext.koin.androidContext
import org.koin.core.context.startKoin

/**
 * 进程级装配。
 *
 * 依赖关系：Keystore → SQLCipher 库 → Ktor → AppState。
 * 注意：数据库密钥只在 createReaderStore 内部短暂以 ByteArray 形式存在。
 */
class ReaderApplication : Application() {

    lateinit var state: AppState
        private set

    override fun onCreate() {
        super.onCreate()
        val koin = startKoin {
            androidContext(this@ReaderApplication)
            modules(platformModule(this@ReaderApplication), appModule())
        }.koin
        val ctx = this
        state = AppState(
            api = koin.get<NovelApi>(),
            keyStore = koin.get<DeviceKeyStore>(),
            storeFactory = { createReaderStore(ctx, koin.get<DeviceKeyStore>()) },
            wiper = koin.get<LocalDataWiper>(),
            sinkFactory = koin.get<AudioSinkFactory>(),
            json = koin.get<Json>(),
            scope = koin.get<CoroutineScope>(),
        )
    }
}
