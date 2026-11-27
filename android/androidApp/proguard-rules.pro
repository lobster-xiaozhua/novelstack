# Ktor / OkHttp
-dontwarn okhttp3.**
-dontwarn okio.**
-dontwarn org.slf4j.**
-keep class io.ktor.** { *; }
-keep class kotlinx.coroutines.** { *; }

# kotlinx.serialization：保留生成的序列化器
-keepattributes *Annotation*, InnerClasses
-keepclassmembers class kotlinx.serialization.json.** { *; }
-keep,includedescriptorclasses class com.novelstack.reader.**$$serializer { *; }
-keepclassmembers class com.novelstack.reader.** {
    *** Companion;
}
-keepclasseswithmembers class com.novelstack.reader.** {
    kotlinx.serialization.KSerializer serializer(...);
}

# SQLDelight 生成代码
-keep class com.novelstack.reader.db.** { *; }

# SQLCipher（JNI 入口按名字反射）
-keep class net.zetetic.database.** { *; }
-keep class net.sqlcipher.** { *; }
-dontwarn net.zetetic.database.**

# Koin
-keep class org.koin.** { *; }
-dontwarn org.koin.**

# 崩溃报告里不保留源码行号（避免把正文片段带进堆栈字符串）
-keepattributes SourceFile,LineNumberTable
-renamesourcefileattribute SourceFile

# Ktor 的 IDE 调试探测引用 JVM 专有类（java.lang.management），Android 运行时没有：
# 只关掉 R8 的 missing-class 告警，不引入这些类。
-dontwarn java.lang.management.**

# WorkManager 通过反射按类名实例化 Worker / WorkerFactory：
# 混淆后类名变化会导致 "Could not instantiate ..." 运行时崩溃。
-keep class com.novelstack.reader.telemetry.TelemetryUploadWorker { *; }
-keep class com.novelstack.reader.telemetry.TelemetryCleanupWorker { *; }

# 遥测库生成的 SQLDelight 接口（与业务库同样的理由）
-keep class com.novelstack.reader.telemetry.db.** { *; }
