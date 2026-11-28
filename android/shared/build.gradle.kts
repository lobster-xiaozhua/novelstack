import org.jetbrains.kotlin.gradle.dsl.JvmTarget

// 与 androidApp 同源：CI 打 tag 时由 NOVELSTACK_VERSION 注入，本地构建回落到 0.1.0。
val sharedVersionName = System.getenv("NOVELSTACK_VERSION")?.takeIf { it.isNotBlank() } ?: "0.1.0"

plugins {
    alias(libs.plugins.kotlin.multiplatform)
    alias(libs.plugins.android.library)
    alias(libs.plugins.kotlin.serialization)
    alias(libs.plugins.compose.multiplatform)
    alias(libs.plugins.kotlin.compose)
    alias(libs.plugins.sqldelight)
}

kotlin {
    androidTarget {
        compilerOptions { jvmTarget.set(JvmTarget.JVM_17) }
    }

    // iOS target 只在 macOS 上声明：Linux/CI 无法编译 Apple 目标，
    // 但 iosMain 源码保留（Keystore/Keychain 的 actual 实现），
    // 在 macOS 上打开即参与编译。第五阶段不验收 iOS。
    val isMac = System.getProperty("os.name").startsWith("Mac")
    if (isMac) {
        iosX64()
        iosArm64()
        iosSimulatorArm64()
    }

    sourceSets {
        commonMain.dependencies {
            implementation(compose.runtime)
            implementation(compose.foundation)
            implementation(compose.material3)
            implementation(compose.ui)

            implementation(libs.ktor.client.core)
            implementation(libs.ktor.client.content.negotiation)
            implementation(libs.ktor.serialization.json)
            implementation(libs.ktor.client.logging)

            // api：这些类型出现在 shared 的公开 API 上（AppState 构造参数、Repository 返回值、
            // Koin Module），androidApp 编译时必须能看见，否则会报 “Cannot access class ... Json”。
            api(libs.koin.core)
            api(libs.sqldelight.runtime)
            api(libs.coroutines.core)
            api(libs.serialization.json)
            implementation(libs.datetime)
            // 引导完成标记：DataStore(KMP) + okio 路径，Android/iOS 共用同一份读写逻辑。
            implementation(libs.androidx.datastore.preferences)
        }
        commonTest.dependencies {
            implementation(kotlin("test"))
        }
        // JVM 侧单测：用真实 SQLite 验证遥测队列的合并/淘汰/跨进程持久化。
        androidUnitTest.dependencies {
            implementation(libs.sqldelight.sqlite.driver)
        }
        androidMain.dependencies {
            implementation(libs.ktor.client.okhttp)
            implementation(libs.koin.android)
            implementation(libs.sqldelight.android.driver)
            implementation(libs.androidx.sqlite)
            implementation(libs.androidx.core.ktx)
            implementation(libs.androidx.work.runtime)
            implementation(libs.tinypinyin)
            implementation(libs.biometric)
            implementation(libs.androidx.graphics.shapes)
            implementation(libs.sqlcipher.android)
            implementation(libs.coroutines.android)
        }
        if (isMac) {
            iosMain.dependencies {
                implementation(libs.ktor.client.core)
            }
        }
    }
}

android {
    namespace = "com.novelstack.reader.shared"
    compileSdk = 35
    defaultConfig {
        minSdk = 26
        // 版本号与 androidApp 的 versionName 同源：CI 由 tag 推导后经环境变量注入，
        // 客户端据此自报版本，避免打包后仍报旧版本导致重复提示更新。
        buildConfigField("String", "APP_VERSION", "\"$sharedVersionName\"")
    }
    buildFeatures {
        buildConfig = true
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
}

sqldelight {
    databases {
        create("ReaderDatabase") {
            packageName.set("com.novelstack.reader.db")
            // UPSERT 需要 SQLite 3.24+；默认的 3.18 方言会报 “',' expected, got 'ON'”。
            dialect("app.cash.sqldelight:sqlite-3-38-dialect:${libs.versions.sqldelight.get()}")
        }
        // 第九阶段：遥测队列独立库。srcDirs 必须与业务库分开，
        // 否则 SQLDelight 会把两个库的 .sq 混在同一个 schema 里。
        create("TelemetryDatabase") {
            packageName.set("com.novelstack.reader.telemetry.db")
            srcDirs.setFrom("src/commonMain/sqldelight-telemetry")
            dialect("app.cash.sqldelight:sqlite-3-38-dialect:${libs.versions.sqldelight.get()}")
        }
    }
}
