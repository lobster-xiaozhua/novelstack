package com.novelstack.reader.app

/**
 * 当前安装包的版本号，由构建系统注入（见 `shared/build.gradle.kts` 的 `APP_VERSION` 字段，
 * 取自环境变量 `NOVELSTACK_VERSION`，CI 由 tag 推导）。
 *
 * 为什么不在 commonMain 写死常量：release 包的 versionName 会随 tag 变化，
 * 若客户端自报版本固定为 0.1.0，装上 0.2.0 后 `isNewer` 仍判定「有新版本」，
 * 用户会被反复提示升级，形成死循环。
 */
expect fun appVersionName(): String
