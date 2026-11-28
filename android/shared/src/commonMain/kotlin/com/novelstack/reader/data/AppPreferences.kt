package com.novelstack.reader.data

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.PreferenceDataStoreFactory
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.booleanPreferencesKey
import androidx.datastore.preferences.core.edit
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map
import okio.Path.Companion.toPath

/**
 * 新手引导的本地状态（DataStore）。
 *
 * 为什么不塞进 SQLCipher 库：
 *  - 引导状态不是隐私数据，也不该被「退出并清除本地数据」误删——用户重新登录后
 *    不该又被欢迎页拦一次；
 *  - 它要在数据库打开之前就能读到（登录前可能就需要判断）；
 *  - DataStore 是原子的、协程友好的，改一个布尔值不用开事务。
 *
 * 读写都走 Flow / suspend，UI 侧用 collectAsState 观察，避免主线程 IO。
 */
class AppPreferences(
    scope: CoroutineScope,
    filePath: String,
) {
    private val store: DataStore<Preferences> =
        PreferenceDataStoreFactory.createWithPath(scope = scope) { filePath.toPath() }

    /** 欢迎页是否已经看完（或跳过）。 */
    val onboarded: Flow<Boolean> = store.data.map { it[KEY_ONBOARD] ?: false }

    /** 书架 / 发现页的蒙层引导是否已经走完。 */
    val coachDone: Flow<Boolean> = store.data.map { it[KEY_COACH] ?: false }

    suspend fun completeOnboarding() {
        store.edit { it[KEY_ONBOARD] = true }
    }

    suspend fun completeCoach() {
        store.edit { it[KEY_COACH] = true }
    }

    /** 设置页「重看新手引导」：清掉两个标记，下次进入重新走一遍。 */
    suspend fun resetGuides() {
        store.edit {
            it.remove(KEY_ONBOARD)
            it.remove(KEY_COACH)
        }
    }

    private companion object {
        val KEY_ONBOARD = booleanPreferencesKey("onboarding_done_v1")
        val KEY_COACH = booleanPreferencesKey("coach_done_v1")
    }
}
