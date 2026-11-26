package com.novelstack.reader.ui

import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import com.novelstack.reader.app.AppState
import com.novelstack.reader.app.Screen

@Composable
fun NovelApp(state: AppState) {
    val screen by state.screen.collectAsState()
    val settings by state.settings.collectAsState()
    val dark = settings.background == "dark"
    NovelTheme(dark = dark) {
        when (val s = screen) {
            Screen.Login -> LoginScreen(state)
            Screen.Library -> LibraryScreen(state)
            Screen.Search -> SearchScreen(state)
            Screen.Settings -> SettingsScreen(state)
            is Screen.Reader -> ReaderScreen(state, s.bookId)
        }
    }
}
