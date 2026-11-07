package space.cybermesh.wallpaper.ui

import android.content.Intent
import android.net.Uri
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.launch
import space.cybermesh.wallpaper.net.Api
import space.cybermesh.wallpaper.net.Session
import space.cybermesh.wallpaper.net.SessionData
import space.cybermesh.wallpaper.net.Update
import space.cybermesh.wallpaper.net.friendlyError

@Composable
fun SettingsScreen(session: Session) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val sessionData = session.state.value

    var codeInput by remember { mutableStateOf("") }
    var msg by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    var createdCode by remember { mutableStateOf<Pair<String, String>?>(null) } // code, url
    // 已配对(服务端有 PC 设备即 true): 隐藏配对框, 防重复配对
    var paired by remember { mutableStateOf<Boolean?>(null) }
    LaunchedEffect(sessionData.token, sessionData.userId) {
        if (sessionData.token.isNotEmpty()) {
            paired = try {
                Api.profile(sessionData.token).paired
            } catch (e: Exception) {
                false
            }
        }
    }

    Column(
        modifier = Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Text("设置", style = MaterialTheme.typography.headlineSmall)

        Text("设备信息", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
        val info = session.deviceInfo()
        InfoRow("型号", info.model)
        InfoRow("系统", "${info.os} ${info.osVersion}")
        InfoRow("应用版本", info.appVersion)
        InfoRow("屏幕", "${info.screenW} × ${info.screenH}")
        InfoRow("设备 ID", sessionData.deviceId)
        InfoRow("身份", if (sessionData.loggedIn) sessionData.userId else "未连接")

        Text("与电脑端共享偏好", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
        if (paired == true) {
            Text(
                "✓ 已与电脑端配对",
                style = MaterialTheme.typography.titleMedium,
                color = MaterialTheme.colorScheme.secondary,
            )
            Spacer(Modifier.height(4.dp))
            Text(
                "匿名用户ID: ${sessionData.userId}",
                style = MaterialTheme.typography.bodyMedium,
            )
            Text(
                "偏好与电脑端实时共享，无需重复配对。",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        } else {
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp), modifier = Modifier.fillMaxWidth()) {
                OutlinedTextField(
                    value = codeInput,
                    onValueChange = { codeInput = it },
                    label = { Text("输入电脑端配对码") },
                    singleLine = true,
                    modifier = Modifier.weight(1f),
                )
            }
            Spacer(Modifier.height(8.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                Button(
                    enabled = !busy && codeInput.isNotBlank(),
                    onClick = {
                        scope.launch {
                            busy = true
                            msg = null
                            try {
                                val r = Api.linkRedeem(codeInput, sessionData.deviceId, session.deviceInfo())
                                session.save(r.userId, r.token)
                                codeInput = ""
                                paired = true
                                msg = "已连接电脑端身份，偏好将自动共享"
                            } catch (e: Exception) {
                                msg = friendlyError(e)
                            }
                            busy = false
                        }
                    },
                ) { Text("输入配对码") }
                OutlinedButton(
                    enabled = !busy && sessionData.loggedIn,
                    onClick = {
                        scope.launch {
                            busy = true
                            msg = null
                            try {
                                val r = Api.linkCreate(sessionData.token)
                                createdCode = r.code to r.url
                            } catch (e: Exception) {
                                msg = "生成失败: ${e.message}"
                            }
                            busy = false
                        }
                    },
                ) { Text("生成配对码") }
            }
        }
        msg?.let { Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant) }

        Text("更新", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            OutlinedButton(
                enabled = !busy,
                onClick = {
                    scope.launch {
                        busy = true
                        msg = null
                        try {
                            val upd = Update.fetchIfUpdate(ctx, session.currentVersion())
                            if (upd == null) {
                                msg = "已是最新版本 (${session.currentVersion()})"
                            } else {
                                Update.install(ctx, upd.second)
                            }
                        } catch (e: Exception) {
                            msg = friendlyError(e)
                        }
                        busy = false
                    }
                },
            ) { Text("检查更新") }
        }

        Text("关于", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
        Text(
            "AI 壁纸工坊 · 为你的桌面与锁屏每天送上新鲜感\n开源免费 · 零本地资源占用\ncybermesh.space",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }

    createdCode?.let { (code, url) ->
        AlertDialog(
            onDismissRequest = { createdCode = null },
            title = { Text("配对码") },
            text = {
                Column {
                    Text(code, style = MaterialTheme.typography.displaySmall, color = MaterialTheme.colorScheme.primary)
                    Spacer(Modifier.height(8.dp))
                    Text(
                        "5 分钟内有效，仅可使用一次。\n在电脑端 设置 → 同步 中输入此码，" +
                            "两端即共享同一份偏好。",
                        style = MaterialTheme.typography.bodySmall,
                    )
                }
            },
            confirmButton = {
                TextButton(onClick = {
                    createdCode = null
                    val intent = Intent(Intent.ACTION_VIEW, Uri.parse(url))
                    runCatching { ctx.startActivity(intent) }
                }) { Text("打开页面") }
            },
            dismissButton = {
                TextButton(onClick = { createdCode = null }) { Text("关闭") }
            },
        )
    }

}

@Composable
private fun InfoRow(k: String, v: String) {
    Row(modifier = Modifier.fillMaxWidth()) {
        Text(
            k,
            style = MaterialTheme.typography.bodyMedium,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            modifier = Modifier.weight(1f),
        )
        Text(v, style = MaterialTheme.typography.bodyMedium)
    }
}
