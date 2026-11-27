package com.novelstack.reader.app

import com.novelstack.reader.shared.BuildConfig

actual fun appVersionName(): String =
    BuildConfig.APP_VERSION.takeIf { it.isNotBlank() } ?: "0.1.0"
