package com.novelstack.reader.di

import android.content.Context
import com.novelstack.reader.net.AndroidEndpointStore
import com.novelstack.reader.net.EndpointStore
import com.novelstack.reader.net.createHttpClient
import com.novelstack.reader.platform.AndroidAudioSinkFactory
import com.novelstack.reader.platform.AndroidDataWiper
import com.novelstack.reader.platform.AndroidDeviceKeyStore
import com.novelstack.reader.platform.AudioSinkFactory
import com.novelstack.reader.platform.DeviceKeyStore
import com.novelstack.reader.platform.LocalDataWiper
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import org.koin.dsl.module

/** Android 平台绑定：Keystore、SQLCipher 由调用方用 createReaderStore 打开。 */
fun platformModule(context: Context) = module {
    single { createHttpClient() }
    single<EndpointStore> { AndroidEndpointStore(context, get()) }
    single<DeviceKeyStore> { AndroidDeviceKeyStore(context) }
    single<LocalDataWiper> { AndroidDataWiper(context) }
    single<AudioSinkFactory> { AndroidAudioSinkFactory() }
    single { CoroutineScope(SupervisorJob() + Dispatchers.Main) }
}
