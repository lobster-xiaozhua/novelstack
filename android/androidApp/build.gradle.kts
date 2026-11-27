import java.util.Base64
import java.util.Properties

plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.compose.multiplatform)
    alias(libs.plugins.kotlin.compose)
}

// 第六阶段：release 签名模板。文件缺失时 release 仍可构建（未签名），不阻塞 CI。
// 真实 keystore.properties 与 .jks 已加入 .gitignore，绝不入库/进备份包。
//
// 第八阶段：CI（release-apk.yml）没有 keystore.properties，改用环境变量注入：
//   NOVELSTACK_KEYSTORE_B64   keystore 文件的 base64（写进临时文件再引用）
//   NOVELSTACK_STORE_PASSWORD / NOVELSTACK_KEY_ALIAS / NOVELSTACK_KEY_PASSWORD
// 优先级：keystore.properties > 环境变量 > 未签名。
val keystorePropertiesFile = rootProject.file("keystore.properties")
val keystoreProperties = Properties().apply {
    if (keystorePropertiesFile.isFile) keystorePropertiesFile.inputStream().use { load(it) }
}
val envKeystoreB64 = System.getenv("NOVELSTACK_KEYSTORE_B64").orEmpty()
val envKeystore = if (envKeystoreB64.isNotBlank()) {
    rootProject.layout.buildDirectory.dir("ci-signing").get().asFile.apply { mkdirs() }
        .resolve("release.jks")
        .also { it.writeBytes(Base64.getDecoder().decode(envKeystoreB64)) }
} else {
    null
}
val hasReleaseKeystore = (keystorePropertiesFile.isFile &&
    !keystoreProperties.getProperty("storeFile").isNullOrBlank()) || envKeystore != null
val ciStorePassword = System.getenv("NOVELSTACK_STORE_PASSWORD").orEmpty()
val ciKeyAlias = System.getenv("NOVELSTACK_KEY_ALIAS").orEmpty()
val ciKeyPassword = System.getenv("NOVELSTACK_KEY_PASSWORD").orEmpty()

// 版本号由 tag/环境变量注入，避免 release 包永远自报 0.1.0 导致应用内更新反复提示。
// 用法：NOVELSTACK_VERSION=0.2.0 NOVELSTACK_VERSION_CODE=200
val appVersionName = System.getenv("NOVELSTACK_VERSION")?.takeIf { it.isNotBlank() } ?: "0.1.0"
val appVersionCode = System.getenv("NOVELSTACK_VERSION_CODE")?.toIntOrNull() ?: 1

android {
    namespace = "com.novelstack.reader"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.novelstack.reader"
        minSdk = 26
        targetSdk = 35
        versionCode = appVersionCode
        versionName = appVersionName
    }

    signingConfigs {
        create("release") {
            if (keystorePropertiesFile.isFile && !keystoreProperties.getProperty("storeFile").isNullOrBlank()) {
                storeFile = rootProject.file(keystoreProperties.getProperty("storeFile"))
                storePassword = keystoreProperties.getProperty("storePassword")
                keyAlias = keystoreProperties.getProperty("keyAlias")
                keyPassword = keystoreProperties.getProperty("keyPassword")
            } else if (envKeystore != null) {
                storeFile = envKeystore
                storePassword = ciStorePassword
                keyAlias = ciKeyAlias
                keyPassword = ciKeyPassword
            }
        }
    }

    buildTypes {
        debug {
            applicationIdSuffix = ".debug"
            isMinifyEnabled = false
        }
        release {
            if (hasReleaseKeystore) signingConfig = signingConfigs.getByName("release")
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions {
        jvmTarget = "17"
    }
    buildFeatures {
        compose = true
    }
    packaging {
        resources.excludes += "/META-INF/{AL2.0,LGPL2.1}"
        resources.excludes += "/META-INF/versions/9/previous-compilation-data.bin"
        jniLibs.useLegacyPackaging = false
    }
}

dependencies {
    implementation(project(":shared"))
    implementation(compose.runtime)
    implementation(compose.foundation)
    implementation(compose.material3)
    implementation(compose.ui)
    implementation(libs.androidx.activity.compose)
    implementation(libs.biometric)
    implementation(libs.androidx.fragment)
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.media)
    // 第十阶段：扫描控制台配对二维码（ZXing，纯本地解码，不上传图像）。
    implementation(libs.zxing.android)
    implementation(libs.koin.android)
    debugImplementation(compose.uiTooling)
}
