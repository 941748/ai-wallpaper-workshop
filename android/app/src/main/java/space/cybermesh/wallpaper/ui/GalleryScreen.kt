package space.cybermesh.wallpaper.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
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
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.Image
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.TextButton
import kotlinx.coroutines.launch
import space.cybermesh.wallpaper.net.Api
import space.cybermesh.wallpaper.net.RenderEngine
import space.cybermesh.wallpaper.net.RenderState
import space.cybermesh.wallpaper.net.Session
import space.cybermesh.wallpaper.net.Update
import space.cybermesh.wallpaper.net.friendlyError
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

private fun stageText(s: RenderState): String = when (s) {
    is RenderState.Idle -> ""
    is RenderState.Planning -> "正在规划画面…"
    is RenderState.Rendering -> "云端生成中… ${s.elapsedSec}s"
    is RenderState.Downloading -> "下载成品…"
    is RenderState.Applying -> "设置壁纸…"
    is RenderState.Done -> "已设置为壁纸 ✔"
    is RenderState.Error -> s.msg
}

/** 进度文案: 阶段标识(牌堆按阶段洗牌)。 */
private fun stageIdOf(s: RenderState): String = when (s) {
    is RenderState.Planning -> "plan"
    is RenderState.Rendering -> "render"
    is RenderState.Downloading -> "down"
    is RenderState.Applying -> "apply"
    else -> ""
}

/** 各阶段文案池(进入阶段时洗牌, 轮播不重复; {t} 占位替换为生成秒数)。 */
private fun poolForStage(stage: String): List<String> = when (stage) {
    "plan" -> listOf(
        "正在分析你的偏好…",
        "AI 正在构思画什么…",
        "从你的风格包里挑选手牌…",
        "正在组合今天的画面…",
        "翻翻你最近喜欢的风格…",
        "给画面挑一束好光线…",
        "让你的画像提供点灵感…",
        "构思一个不重样的构图…",
        "正在酝酿一个新点子…",
        "帮你避开昨天的审美…",
        "挑个你没见过的角度…",
        "今天也要独一无二…",
    )
    "render" -> listOf(
        "云端生成中… {t}s",
        "GPU 正在渲染细节…",
        "光影与上色处理中…",
        "一笔一笔画出来…",
        "空气感正在到位…",
        "质感逐渐清晰…",
        "高清细节放大中…",
        "快了，别眨眼…",
        "收尾的高光时刻…",
        "颜料还没干，稍等…",
        "曲线和色彩正在碰撞…",
        "{t}s · 马上见面…",
    )
    "down" -> listOf(
        "正在下载成品…",
        "把新画装进手机…",
        "信号在路上狂奔…",
        "传输中，马上见面…",
        "新壁纸正在过安检…",
    )
    "apply" -> listOf(
        "正在设置壁纸…",
        "桌面与锁屏一起换新…",
        "正在给桌面铺上新画…",
        "最后一步，揭幕…",
        "换上新装…",
    )
    else -> listOf(stageText(RenderState.Idle))
}

/** 画廊: 当前壁纸 + 换一张(完整出图链路) + 最近生成点按即设、长按删除。 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun GalleryScreen(session: Session) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()

    var state by remember { mutableStateOf<RenderState>(RenderState.Idle) }
    var hero by remember { mutableStateOf<Any?>(null) } // Bitmap 或 File
    var recent by remember { mutableStateOf<List<File>>(emptyList()) }
    var quotaMsg by remember { mutableStateOf<String?>(null) }
    var pendingInstall by remember { mutableStateOf<Pair<String, File>?>(null) }
    var confirmDelete by remember { mutableStateOf<File?>(null) } // 长按待删除项
    var updBusy by remember { mutableStateOf(false) }

    // 出图时并行检查更新: 静默下载, 出图完成后弹"立即安装"
    fun checkUpdateInBackground() {
        if (updBusy || pendingInstall != null) return
        updBusy = true
        scope.launch {
            try {
                pendingInstall = Update.fetchIfUpdate(ctx, session.currentVersion())
            } catch (_: Exception) {
                // 更新检查失败静默, 不影响出图
            }
            updBusy = false
        }
    }

    fun refresh() {
        hero = RenderEngine.currentWallpaper(ctx)
        recent = RenderEngine.recent(ctx)
    }
    LaunchedEffect(Unit) { refresh() }

    val busy = state !is RenderState.Done && state !is RenderState.Error && state !is RenderState.Idle

    // 进度文案轮播: 每 1.5s 换一条; 按阶段洗牌成牌堆, 走完一轮重洗, 尽量不重复
    var msgTick by remember { mutableStateOf(0) }
    var msgDeck by remember { mutableStateOf<List<String>>(emptyList()) }
    var msgDeckStage by remember { mutableStateOf("") }
    val stage = stageIdOf(state)
    LaunchedEffect(stage, busy) {
        if (busy && stage.isNotEmpty()) {
            msgDeck = poolForStage(stage).shuffled()
            msgDeckStage = stage
            msgTick = 0
        }
    }
    LaunchedEffect(busy) {
        if (busy) {
            while (true) {
                kotlinx.coroutines.delay(1500)
                msgTick++
                if (msgDeck.isNotEmpty() && msgTick % msgDeck.size == 0) {
                    msgDeck = msgDeck.shuffled() // 一轮走完重新洗牌, 下轮顺序不同
                }
            }
        }
    }
    val progressMsg = if (busy && msgDeckStage == stage && msgDeck.isNotEmpty()) {
        val raw = msgDeck[msgTick % msgDeck.size]
        if (raw.contains("{t}")) {
            raw.replace("{t}", (state as? RenderState.Rendering)?.elapsedSec?.toString() ?: "…")
        } else {
            raw
        }
    } else {
        stageText(state)
    }

    Column(
        modifier = Modifier.fillMaxSize().padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp),
    ) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("画廊", style = MaterialTheme.typography.headlineSmall, modifier = Modifier.weight(1f))
            Button(
                enabled = !busy,
                onClick = {
                    checkUpdateInBackground()
                    scope.launch {
                        quotaMsg = null
                        try {
                            // 断点续传: 15 分钟内的失败任务只重试"轮询+下载+设置"段, 不重新出图
                            val pending = session.lastJob()
                            val parts = pending.split("|")
                            val now = System.currentTimeMillis()
                            val resumeId = if (parts.size == 2 &&
                                now - (parts[1].toLongOrNull() ?: 0L) < 15 * 60 * 1000L
                            ) parts[0] else ""
                            val jobId = if (resumeId.isNotEmpty()) {
                                state = RenderState.Rendering(0)
                                resumeId
                            } else {
                                val id = RenderEngine.startJob(ctx, session) { state = it }
                                session.setLastJob("$id|$now")
                                id
                            }
                            RenderEngine.finishJob(ctx, session, jobId) { state = it }
                            session.setLastJob("")
                            refresh()
                        } catch (e: Exception) {
                            val msg = friendlyError(e)
                            state = RenderState.Error(msg)
                            if (msg.contains("没画好") || msg.contains("找不到")) {
                                session.setLastJob("") // 任务本身失败 → 废弃, 下次全新出图
                            }
                        }
                    }
                },
            ) { Text("换一张") }
        }

        val heroBmp = hero as? android.graphics.Bitmap
        val heroH = when {
            heroBmp != null -> 280.dp
            busy -> 150.dp
            else -> 0.dp
        }
        if (heroH > 0.dp) {
            Box(
                modifier = Modifier
                    .fillMaxWidth()
                    .height(heroH)
                    .clip(RoundedCornerShape(16.dp))
                    .background(MaterialTheme.colorScheme.surface),
                contentAlignment = Alignment.Center,
            ) {
                if (heroBmp != null) {
                    Image(
                        bitmap = heroBmp.asImageBitmap(),
                        contentDescription = "当前壁纸",
                        contentScale = ContentScale.Crop,
                        modifier = Modifier.fillMaxSize(),
                    )
                    if (busy) {
                        Box(
                            modifier = Modifier
                                .fillMaxSize()
                                .background(MaterialTheme.colorScheme.background.copy(alpha = 0.72f)),
                            contentAlignment = Alignment.Center,
                        ) {
                            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                                CircularProgressIndicator()
                                Spacer(Modifier.height(10.dp))
                                Text(progressMsg)
                            }
                        }
                    }
                } else {
                    // 空态 + 生成中: 矮框进度, 不再整块留白
                    Column(horizontalAlignment = Alignment.CenterHorizontally) {
                        CircularProgressIndicator()
                        Spacer(Modifier.height(10.dp))
                        Text(progressMsg)
                    }
                }
            }
        } else {
            Text(
                "还没有壁纸 — 点右上角「换一张」生成你的第一张",
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                textAlign = TextAlign.Center,
                modifier = Modifier.fillMaxWidth().padding(vertical = 28.dp),
            )
        }

        when (val s = state) {
            is RenderState.Error -> Text(s.msg, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall)
            is RenderState.Done -> Text(stageText(s), color = MaterialTheme.colorScheme.secondary, style = MaterialTheme.typography.bodySmall)
            else -> {}
        }
        quotaMsg?.let { Text(it, color = MaterialTheme.colorScheme.error, style = MaterialTheme.typography.bodySmall) }

        // 长按删除确认: 移出本地「最近生成」并同步清理云端副本(旧时间戳名反查不到任务, 只删本地)
        confirmDelete?.let { f ->
            val mid = f.name.removePrefix("wp-").removeSuffix(".jpg")
            val jobId = mid.takeIf { s ->
                s.length == 24 && s.all { it in '0'..'9' || it in 'a'..'f' || it in 'A'..'F' }
            }
            AlertDialog(
                onDismissRequest = { confirmDelete = null },
                title = { Text("删除这张图片？") },
                text = {
                    Text(
                        if (jobId != null) "将从「最近生成」中移除并清理云端副本，删除后无法恢复。已设置的壁纸不受影响。"
                        else "将从「最近生成」中移除，删除后无法恢复。已设置的壁纸不受影响。"
                    )
                },
                confirmButton = {
                    TextButton(onClick = {
                        confirmDelete = null
                        scope.launch {
                            if (jobId != null) {
                                runCatching { Api.deleteImage(session.state.value.token, jobId) }
                            }
                            f.delete()
                            refresh()
                        }
                    }) { Text("删除", color = MaterialTheme.colorScheme.error) }
                },
                dismissButton = {
                    TextButton(onClick = { confirmDelete = null }) { Text("取消") }
                },
            )
        }

        // 出图期间静默下载好的新版本: 出图结束再弹安装(不打断生成过程)
        pendingInstall?.let { (ver, file) ->
            if (!busy) {
                AlertDialog(
                    onDismissRequest = { pendingInstall = null },
                    title = { Text("新版本已就绪") },
                    text = { Text("v$ver 已在出图时自动下载完成，安装后立即生效。") },
                    confirmButton = {
                        TextButton(onClick = {
                            pendingInstall = null
                            runCatching { Update.install(ctx, file) }
                        }) { Text("立即安装") }
                    },
                    dismissButton = {
                        TextButton(onClick = { pendingInstall = null }) { Text("稍后") }
                    },
                )
            }
        }

        if (recent.isNotEmpty()) {
            Text("最近生成 · 点按即设为壁纸", style = MaterialTheme.typography.titleSmall, color = MaterialTheme.colorScheme.primary)
            LazyVerticalGrid(
                columns = GridCells.Fixed(3),
                horizontalArrangement = Arrangement.spacedBy(8.dp),
                verticalArrangement = Arrangement.spacedBy(8.dp),
                modifier = Modifier.weight(1f),
            ) {
                items(recent) { f ->
                    val applying = remember { mutableStateOf(false) }
                    Box(
                        modifier = Modifier
                            .aspectRatio(9f / 16f)
                            .clip(RoundedCornerShape(10.dp))
                            .background(MaterialTheme.colorScheme.surface)
                            .combinedClickable(
                                onClick = {
                                    if (!applying.value && !busy) {
                                        scope.launch {
                                            applying.value = true
                                            try {
                                                RenderEngine.setFile(ctx, f)
                                                hero = RenderEngine.currentWallpaper(ctx)
                                            } catch (e: Exception) {
                                                quotaMsg = friendlyError(e)
                                            }
                                            applying.value = false
                                        }
                                    }
                                },
                                onLongClick = { confirmDelete = f },
                            ),
                        contentAlignment = Alignment.Center,
                    ) {
                        CoilThumb(f)
                        if (applying.value) {
                            CircularProgressIndicator(Modifier.width(22.dp))
                        }
                    }
                }
            }
        } else {
            Spacer(Modifier.weight(1f))
            Text(
                "生成过的壁纸会保留在这里",
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                modifier = Modifier.align(Alignment.CenterHorizontally),
            )
        }
    }
}

@Composable
private fun CoilThumb(f: File) {
    val bmp = remember(f) {
        runCatching {
            val opts = android.graphics.BitmapFactory.Options().apply { inSampleSize = 4 }
            android.graphics.BitmapFactory.decodeFile(f.absolutePath, opts)
        }.getOrNull()
    }
    if (bmp != null) {
        Image(
            bitmap = bmp.asImageBitmap(),
            contentDescription = null,
            contentScale = ContentScale.Crop,
            modifier = Modifier.fillMaxSize(),
        )
    } else {
        Text("…", color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}
