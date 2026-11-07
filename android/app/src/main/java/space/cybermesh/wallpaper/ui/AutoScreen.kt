package space.cybermesh.wallpaper.ui

import android.content.Intent
import android.net.Uri
import android.provider.Settings
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Switch
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.work.Constraints
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import kotlinx.coroutines.launch
import space.cybermesh.wallpaper.net.Session
import space.cybermesh.wallpaper.worker.DailySwapWorker
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.concurrent.TimeUnit

private const val DAILY_WORK = "aw-daily-swap"

/** 每日自动换: WorkManager 唯一周期任务(每天一轮, 系统调度, 不常驻不轮询)。 */
@Composable
fun AutoScreen(session: Session) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var enabled by remember { mutableStateOf<Boolean?>(null) }
    var last by remember { mutableStateOf("") }

    LaunchedEffect(Unit) {
        enabled = session.autoEnabled()
        last = session.autoLast()
    }

    Column(
        modifier = Modifier.fillMaxSize().padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Text("每日自动换", style = MaterialTheme.typography.headlineSmall)

        Row(
            verticalAlignment = Alignment.CenterVertically,
            modifier = Modifier
                .fillMaxWidth()
                .background(MaterialTheme.colorScheme.surface, RoundedCornerShape(16.dp))
                .padding(horizontal = 16.dp, vertical = 12.dp),
        ) {
            Column(Modifier.weight(1f)) {
                Text("自动换图", style = MaterialTheme.typography.titleMedium)
                Text(
                    "每天生成并设置一张新壁纸（桌面+锁屏）",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Switch(
                checked = enabled == true,
                enabled = enabled != null,
                onCheckedChange = { on ->
                    scope.launch {
                        session.setAutoEnabled(on)
                        enabled = on
                        val wm = WorkManager.getInstance(ctx)
                        if (on) {
                            val req = PeriodicWorkRequestBuilder<DailySwapWorker>(1, TimeUnit.DAYS)
                                .setConstraints(
                                    Constraints.Builder()
                                        .setRequiredNetworkType(NetworkType.CONNECTED)
                                        .build(),
                                )
                                .build()
                            wm.enqueueUniquePeriodicWork(DAILY_WORK, ExistingPeriodicWorkPolicy.KEEP, req)
                        } else {
                            wm.cancelUniqueWork(DAILY_WORK)
                        }
                        last = session.autoLast()
                    }
                },
            )
        }

        if (enabled == true) {
            Text(
                "已开启 · 每 24 小时由系统调度执行一次；不常驻、不轮询，耗电近乎为零。",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.secondary,
            )
            Text(
                "消耗移动端独立配额（12 张/天），与电脑端互不影响。",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }

        val parts = last.split("|")
        if (parts.size == 2) {
            val ts = parts[0].toLongOrNull() ?: 0L
            val time = if (ts > 0) SimpleDateFormat("MM-dd HH:mm", Locale.getDefault()).format(Date(ts)) else "—"
            val rawRes = parts[1]
            val ok = rawRes == "ok"
            val resText = when {
                ok -> "成功"
                rawRes.contains("quota") -> "配额已用完，明日重试"
                rawRes.startsWith("fail:") -> rawRes.removePrefix("fail:").trim()
                else -> rawRes
            }
            Text(
                "上次执行: $time · $resText",
                style = MaterialTheme.typography.bodySmall,
                color = if (ok) MaterialTheme.colorScheme.onSurfaceVariant else MaterialTheme.colorScheme.error,
            )
        }

        Spacer(Modifier.height(4.dp))
        Text(
            "小提示：部分手机系统会严格限制后台任务，建议把本应用加入电池优化白名单，" +
                "以保证每天准时换图。",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        OutlinedButton(
            onClick = {
                val intent = Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:${ctx.packageName}"))
                runCatching { ctx.startActivity(intent) }
            },
        ) { Text("前往电池设置") }

        if (enabled == false) {
            Spacer(Modifier.height(8.dp))
            Text(
                "开启后，即使不打开应用，也会在每天自动完成拉图、下载与设置。",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}
