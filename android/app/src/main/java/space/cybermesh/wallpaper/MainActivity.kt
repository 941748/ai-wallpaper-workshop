package space.cybermesh.wallpaper

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.outlined.Autorenew
import androidx.compose.material.icons.outlined.FavoriteBorder
import androidx.compose.material.icons.outlined.Image
import androidx.compose.material.icons.outlined.Settings
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.vector.ImageVector
import androidx.compose.ui.unit.dp
import androidx.navigation.NavGraph.Companion.findStartDestination
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.currentBackStackEntryAsState
import androidx.navigation.compose.rememberNavController
import space.cybermesh.wallpaper.net.Session
import space.cybermesh.wallpaper.ui.AutoScreen
import space.cybermesh.wallpaper.ui.GalleryScreen
import space.cybermesh.wallpaper.ui.PrefEditor
import space.cybermesh.wallpaper.ui.PrefsScreen
import space.cybermesh.wallpaper.ui.SettingsScreen
import space.cybermesh.wallpaper.ui.theme.AWTheme

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val session = Session(applicationContext)
        setContent {
            AWTheme {
                var ready by remember { mutableStateOf(false) }
                var bootError by remember { mutableStateOf<String?>(null) }
                var retry by remember { mutableStateOf(0) }
                LaunchedEffect(retry) {
                    ready = false
                    bootError = null
                    try {
                        session.load()
                        session.ensureRegistered()
                        ready = true
                    } catch (e: Exception) {
                        bootError = e.message ?: "初始化失败"
                    }
                }
                when {
                    !ready && bootError == null -> Loading()
                    bootError != null -> BootError(bootError!!) { retry++ }
                    else -> AppScaffold(session)
                }
            }
        }
    }
}

@Composable
private fun Loading() {
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        CircularProgressIndicator()
    }
}

@Composable
private fun BootError(msg: String, onRetry: () -> Unit) {
    Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
        Column(horizontalAlignment = Alignment.CenterHorizontally, modifier = Modifier.padding(24.dp)) {
            Text("无法连接云端", style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(6.dp))
            Text(
                msg,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(14.dp))
            Button(onClick = onRetry) { Text("重试") }
        }
    }
}

private data class NavItem(val route: String, val label: String, val icon: ImageVector)

@Composable
private fun AppScaffold(session: Session) {
    val nav = rememberNavController()
    val prefEditor = remember { PrefEditor() } // 状态提升: 切 tab 不丢编辑
    val items = listOf(
        NavItem("gallery", "画廊", Icons.Outlined.Image),
        NavItem("auto", "自动换", Icons.Outlined.Autorenew),
        NavItem("prefs", "偏好", Icons.Outlined.FavoriteBorder),
        NavItem("settings", "设置", Icons.Outlined.Settings),
    )
    Scaffold(
        bottomBar = {
            NavigationBar {
                val backStack by nav.currentBackStackEntryAsState()
                val current = backStack?.destination?.route
                items.forEach { item ->
                    NavigationBarItem(
                        selected = current == item.route,
                        onClick = {
                            nav.navigate(item.route) {
                                popUpTo(nav.graph.findStartDestination().id) { saveState = true }
                                launchSingleTop = true
                                restoreState = true
                            }
                        },
                        icon = { Icon(item.icon, contentDescription = item.label) },
                        label = { Text(item.label) },
                    )
                }
            }
        },
    ) { padding ->
        NavHost(
            navController = nav,
            startDestination = "gallery",
            modifier = Modifier.padding(padding),
        ) {
            composable("gallery") { GalleryScreen(session) }
            composable("auto") { AutoScreen(session) }
            composable("prefs") { PrefsScreen(session, prefEditor) }
            composable("settings") { SettingsScreen(session) }
        }
    }
}
