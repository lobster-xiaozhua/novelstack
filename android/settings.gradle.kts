// 可选镜像：网络无法直连 google()/mavenCentral() 时设置
//   NOVELSTACK_MAVEN_MIRROR=https://<your-maven-mirror>/
// 默认不启用，仓库仍以官方源为准。
// 注意：Gradle 的 settings 脚本里 pluginManagement / dependencyResolutionManagement
// 是独立的脚本块，顶层 val 在块内不可见，因此每块内部各取一次环境变量。

pluginManagement {
    val mirror = System.getenv("NOVELSTACK_MAVEN_MIRROR").orEmpty()
    repositories {
        if (mirror.isNotBlank()) {
            maven { url = uri(mirror) }
        }
        google {
            content {
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("com\\.google.*")
                includeGroupByRegex("androidx.*")
            }
        }
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    val mirror = System.getenv("NOVELSTACK_MAVEN_MIRROR").orEmpty()
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        if (mirror.isNotBlank()) {
            maven { url = uri(mirror) }
        }
        google()
        mavenCentral()
    }
}

rootProject.name = "novelstack-reader"
include(":shared")
include(":androidApp")
