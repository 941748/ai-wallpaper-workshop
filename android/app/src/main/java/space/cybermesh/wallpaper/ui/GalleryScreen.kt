package space.cybermesh.wallpaper.ui

import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.fadeIn
import androidx.compose.animation.fadeOut
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.ExperimentalFoundationApi
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.aspectRatio
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.lazy.grid.itemsIndexed
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.TextButton
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import space.cybermesh.wallpaper.net.Api
import space.cybermesh.wallpaper.net.HistoryItem
import space.cybermesh.wallpaper.net.RenderEngine
import space.cybermesh.wallpaper.net.RenderFlow
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

/** wp-{jobID}.jpg 新命名 → 反查任务ID; 旧时间戳名返回 null(无法关联云端副本)。
 *  P6 起新任务 16 位(8字节), 存量 24 位并存, 两种都认。 */
private fun jobIdOf(f: File): String? {
    val mid = f.name.removePrefix("wp-").removeSuffix(".jpg")
    return mid.takeIf { s -> (s.length == 16 || s.length == 24) && s.all { it in '0'..'9' || it in 'a'..'f' || it in 'A'..'F' } }
}

/** 画廊: 换一张(完整出图链路) + 本地/云端双模点按即设、长按删除、心形收藏。 */
@OptIn(ExperimentalFoundationApi::class)
@Composable
fun GalleryScreen(session: Session) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()

    val state by RenderFlow.state.collectAsState() // 进程级: 切页返回进度仍在
    var recent by remember { mutableStateOf<List<File>>(emptyList()) }
    val quotaMsg by RenderFlow.quotaMsg.collectAsState()
    var pendingInstall by remember { mutableStateOf<Pair<String, File>?>(null) }
    var confirmDelete by remember { mutableStateOf<File?>(null) } // 长按待删除项
    var favs by remember { mutableStateOf(mapOf<String, Boolean>()) } // jobId → 云端收藏态
    var favOnly by remember { mutableStateOf(false) } // 收藏筛选: 只显示红心图
    var updBusy by remember { mutableStateOf(false) }

    // ---------- 双模: 本地(最近生成) / 云端(归档历史) ----------
    var mode by remember { mutableStateOf("local") } // local | cloud
    var cloudItems by remember { mutableStateOf<List<HistoryItem>>(emptyList()) }
    var cloudLoading by remember { mutableStateOf(false) }
    var cloudEnd by remember { mutableStateOf(false) }
    var cloudOffset by remember { mutableStateOf(0) } // 已消费行数(删除后回退 1 防跳行)
    var confirmDeleteCloud by remember { mutableStateOf<HistoryItem?>(null) }

    // ---------- 点按即设反馈(两模式统一): 遮罩转圈 → ✔徽标; 错误走底部悬浮胶囊(零位移) ----------
    var applyingKey by remember { mutableStateOf<String?>(null) } // 进行中瓦片(local=文件名, cloud=jobId)
    var applyingMsg by remember { mutableStateOf<String?>(null) }
    var appliedKey by remember { mutableStateOf<String?>(null) }  // 刚换成功(✔徽标 1.2s)
    var fbText by remember { mutableStateOf<String?>(null) }      // 底部悬浮错误胶囊文案
    var fbTick by remember { mutableStateOf(0) }

    fun toast(msg: String) {
        fbText = msg
        fbTick++
    }

    // 云端历史分页加载(reset=拉首页: 切模式/切收藏筛选时)
    fun loadCloud(reset: Boolean) {
        if (cloudLoading || (!reset && cloudEnd)) return
        cloudLoading = true
        scope.launch {
            try {
                val off = if (reset) 0 else cloudOffset
                val resp = Api.history(session.state.value.token, 30, off, favOnly)
                val page = resp.items
                cloudItems = if (reset) page else cloudItems + page
                cloudOffset = off + page.size
                cloudEnd = page.size < 30
            } catch (e: Exception) {
                toast(friendlyError(e)) // 不置 cloudEnd, 滚回可重试
            } finally {
                cloudLoading = false
            }
        }
    }

    LaunchedEffect(fbTick) {
        if (fbTick > 0) {
            delay(2500)
            fbText = null
        }
    }
    LaunchedEffect(appliedKey) {
        if (appliedKey != null) {
            delay(1200)
            appliedKey = null
        }
    }
    LaunchedEffect(mode, favOnly) {
        if (mode == "cloud") loadCloud(reset = true)
    }

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
        recent = RenderEngine.recent(ctx)
    }

    fun toggleFav(jobId: String, cur: Boolean) {
        val next = !cur
        favs = favs + (jobId to next) // 乐观更新
        scope.launch {
            runCatching { Api.historyFav(session.state.value.token, jobId, next) }
                .onSuccess { resp ->
                    favs = favs + (jobId to resp.fav)
                    session.setFavs(favs.filterValues { it }.keys) // 落缓存(下次打开秒绘)
                }
                .onFailure { favs = favs + (jobId to cur) } // 回滚
        }
    }

    LaunchedEffect(Unit) {
        refresh()
        // 收藏态: 先按本地缓存秒绘♥(空窗消除), 云端返回后整体纠偏; 失败保留缓存
        favs = session.favs().associateWith { true }
        runCatching {
            val resp = Api.history(session.state.value.token, 100)
            val fetched = resp.items.associate { it.jobId to it.fav }
            favs = fetched
            session.setFavs(fetched.filterValues { it }.keys)
        }
    }

    LaunchedEffect(state) {
        // 出图完成(可能在切页期间完成, 返回时补刷)→刷新本地列表与当前壁纸
        if (state is RenderState.Done) refresh()
    }

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

    Box(modifier = Modifier.fillMaxSize()) {
        Column(
            modifier = Modifier.fillMaxSize().padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            // 页头: 标题 + 模式分段(本地|云端) + 收藏筛选 + 换一张
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("画廊", style = MaterialTheme.typography.headlineSmall, modifier = Modifier.weight(1f))
                ModeSegmented(mode) { mode = it }
                Spacer(Modifier.width(8.dp))
                // 收藏筛选: 只显示带红心的图(旧时间戳文件无法关联云端收藏, 不入列)
                OutlinedButton(
                    onClick = { favOnly = !favOnly },
                    enabled = !busy,
                    contentPadding = PaddingValues(horizontal = 10.dp, vertical = 4.dp),
                ) {
                    Text(
                        if (favOnly) "♥" else "♡",
                        color = if (favOnly) Color(0xFFFF4D6A) else MaterialTheme.colorScheme.primary,
                        fontSize = 16.sp,
                    )
                }
                Spacer(Modifier.width(8.dp))
                Button(
                    enabled = !busy,
                    onClick = {
                        checkUpdateInBackground()
                        // 出图流程在进程级执行: 切页不中断进度与下载(见 RenderFlow)
                        RenderFlow.swap(ctx, session)
                    },
                ) { Text("换一张") }
            }

            // 出图进度(矮框, 仅生成中显示); 空闲时只留一行占位文案, 不占高度
            if (busy) {
                Box(
                    modifier = Modifier
                        .fillMaxWidth()
                        .height(150.dp)
                        .clip(RoundedCornerShape(16.dp))
                        .background(MaterialTheme.colorScheme.surface),
                    contentAlignment = Alignment.Center,
                ) {
                    Column(horizontalAlignment = Alignment.CenterHorizontally) {
                        CircularProgressIndicator()
                        Spacer(Modifier.height(10.dp))
                        Text(progressMsg)
                    }
                }
            } else {
                Text(
                    "偏好每维单选一个，再试试「换一张」",
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    textAlign = TextAlign.Center,
                    style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.fillMaxWidth().padding(vertical = 8.dp),
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
            val jobId = jobIdOf(f)
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

        // 云端长按删除确认: 服务端联动删归档行 + OSS 副本
        confirmDeleteCloud?.let { row ->
            AlertDialog(
                onDismissRequest = { confirmDeleteCloud = null },
                title = { Text("删除这张图片？") },
                text = { Text("将从云端历史中移除并清理图片副本，删除后无法恢复。已设置的壁纸不受影响。") },
                confirmButton = {
                    TextButton(onClick = {
                        confirmDeleteCloud = null
                        scope.launch {
                            runCatching { Api.deleteImage(session.state.value.token, row.jobId) }
                                .onSuccess {
                                    cloudItems = cloudItems.filterNot { x -> x.jobId == row.jobId }
                                    cloudOffset = (cloudOffset - 1).coerceAtLeast(0)
                                }
                                .onFailure { toast(friendlyError(it)) }
                        }
                    }) { Text("删除", color = MaterialTheme.colorScheme.error) }
                },
                dismissButton = {
                    TextButton(onClick = { confirmDeleteCloud = null }) { Text("取消") }
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

        // 收藏筛选: 仅新命名(wp-{jobID}.jpg)且云端 fav=true 的图入列
        val shown = if (favOnly) {
            recent.filter { f ->
                jobIdOf(f)?.let { favs[it] == true } == true
            }
        } else {
            recent
        }
        if (mode == "local") {
            if (recent.isNotEmpty()) {
                if (favOnly && shown.isEmpty()) {
                    Spacer(Modifier.weight(1f))
                    Text(
                        "还没有收藏 — 点图片右上角 ♡ 收藏后在这里集中查看",
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.align(Alignment.CenterHorizontally),
                    )
                    Spacer(Modifier.weight(1f))
                    return@Column
                }
                LazyVerticalGrid(
                    columns = GridCells.Fixed(3),
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp),
                    modifier = Modifier.weight(1f),
                ) {
                    items(shown, key = { it.name }) { f ->
                        val key = f.name
                        Box(
                            modifier = Modifier
                                .aspectRatio(9f / 16f)
                                .clip(RoundedCornerShape(10.dp))
                                .background(MaterialTheme.colorScheme.surface)
                                .combinedClickable(
                                    onClick = {
                                        if (applyingKey == null && !busy) {
                                            scope.launch {
                                                applyingKey = key
                                                applyingMsg = "正在换上…"
                                                try {
                                                    RenderEngine.setFile(ctx, f)
                                                    appliedKey = key
                                                } catch (e: Exception) {
                                                    toast(friendlyError(e))
                                                } finally {
                                                    applyingKey = null
                                                }
                                            }
                                        }
                                    },
                                    onLongClick = { confirmDelete = f },
                                ),
                            contentAlignment = Alignment.Center,
                        ) {
                            CoilThumb(f)
                            // 心形收藏: 仅新命名文件可反查云端任务; 旧时间戳名不显示
                            val jobId = jobIdOf(f)
                            if (jobId != null) {
                                val isFav = favs[jobId] == true
                                Text(
                                    text = if (isFav) "♥" else "♡",
                                    color = if (isFav) Color(0xFFFF4D6A) else Color.White,
                                    fontSize = 15.sp,
                                    modifier = Modifier
                                        .align(Alignment.TopEnd)
                                        .padding(4.dp)
                                        .background(Color.Black.copy(alpha = 0.35f), CircleShape)
                                        .clickable(enabled = !busy) { toggleFav(jobId, isFav) }
                                        .padding(horizontal = 5.dp, vertical = 1.dp),
                                )
                            }
                            ApplyOverlay(applyingKey == key, applyingMsg)
                            if (appliedKey == key) AppliedBadge()
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
        } else {
            // ---------- 云端历史(归档): offset 分页, 滚到底加载下一页 ----------
            if (cloudItems.isEmpty()) {
                Spacer(Modifier.weight(1f))
                if (cloudLoading) {
                    CircularProgressIndicator()
                } else {
                    Text(
                        if (favOnly) "还没有收藏的云端图片 — 点图片右上角 ♡ 收藏"
                        else "云端还没有图片",
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        modifier = Modifier.align(Alignment.CenterHorizontally),
                    )
                }
                Spacer(Modifier.weight(1f))
            } else {
                LazyVerticalGrid(
                    columns = GridCells.Fixed(3),
                    horizontalArrangement = Arrangement.spacedBy(8.dp),
                    verticalArrangement = Arrangement.spacedBy(8.dp),
                    modifier = Modifier.weight(1f),
                ) {
                    itemsIndexed(cloudItems, key = { _, row -> row.jobId }) { index, row ->
                        val key = row.jobId
                        Box(
                            modifier = Modifier
                                .aspectRatio(9f / 16f)
                                .clip(RoundedCornerShape(10.dp))
                                .background(MaterialTheme.colorScheme.surface)
                                .combinedClickable(
                                    onClick = {
                                        if (applyingKey == null && !busy) {
                                            scope.launch {
                                                applyingKey = key
                                                applyingMsg = "下载中…"
                                                try {
                                                    RenderEngine.finishFromHistory(ctx, session, key) { st ->
                                                        applyingMsg = when (st) {
                                                            is RenderState.Downloading -> "下载中…"
                                                            is RenderState.Applying -> "正在换上…"
                                                            else -> applyingMsg
                                                        }
                                                    }
                                                    refresh() // 本地列表补上刚落盘的 wp-{jobId}.jpg
                                                    appliedKey = key
                                                } catch (e: Exception) {
                                                    toast(friendlyError(e))
                                                } finally {
                                                    applyingKey = null
                                                }
                                            }
                                        }
                                    },
                                    onLongClick = { confirmDeleteCloud = row },
                                ),
                            contentAlignment = Alignment.Center,
                        ) {
                            CloudThumb(session, key, Modifier.fillMaxSize())
                            val isFav = favs[key] == true
                            Text(
                                text = if (isFav) "♥" else "♡",
                                color = if (isFav) Color(0xFFFF4D6A) else Color.White,
                                fontSize = 15.sp,
                                modifier = Modifier
                                    .align(Alignment.TopEnd)
                                    .padding(4.dp)
                                    .background(Color.Black.copy(alpha = 0.35f), CircleShape)
                                    .clickable(enabled = !busy) { toggleFav(key, isFav) }
                                    .padding(horizontal = 5.dp, vertical = 1.dp),
                            )
                            Text(
                                text = shortTime(row.createdAt),
                                color = Color.White,
                                fontSize = 10.sp,
                                modifier = Modifier
                                    .align(Alignment.BottomStart)
                                    .padding(4.dp)
                                    .background(Color.Black.copy(alpha = 0.35f), RoundedCornerShape(4.dp))
                                    .padding(horizontal = 4.dp, vertical = 1.dp),
                            )
                            ApplyOverlay(applyingKey == key, applyingMsg)
                            if (appliedKey == key) AppliedBadge()
                        }
                        if (index == cloudItems.lastIndex) {
                            LaunchedEffect(Unit) {
                                if (!cloudLoading && !cloudEnd) loadCloud(reset = false)
                            }
                        }
                    }
                }
                if (cloudLoading) {
                    Row(
                        verticalAlignment = Alignment.CenterVertically,
                        modifier = Modifier.fillMaxWidth().padding(vertical = 8.dp),
                        horizontalArrangement = Arrangement.Center,
                    ) {
                        CircularProgressIndicator(Modifier.width(16.dp), strokeWidth = 2.dp)
                        Spacer(Modifier.width(8.dp))
                        Text("加载中…", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
                    }
                }
            }
        }
    }

        // 底部悬浮错误胶囊: 盖在内容上方淡入淡出, 不挤动布局(零位移)
        AnimatedVisibility(
            visible = fbText != null,
            enter = fadeIn(),
            exit = fadeOut(),
            modifier = Modifier
                .align(Alignment.BottomCenter)
                .padding(bottom = 24.dp),
        ) {
            Text(
                fbText ?: "",
                color = Color.White,
                style = MaterialTheme.typography.bodySmall,
                textAlign = TextAlign.Center,
                maxLines = 2,
                modifier = Modifier
                    .widthIn(max = 300.dp)
                    .clip(RoundedCornerShape(50))
                    .background(Color(0xCCB3261E))
                    .padding(horizontal = 16.dp, vertical = 10.dp),
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

/** 模式分段控件: 本地 / 云端(页头内, 选中段高亮)。 */
@Composable
private fun ModeSegmented(mode: String, onChange: (String) -> Unit) {
    Row(
        Modifier
            .clip(RoundedCornerShape(10.dp))
            .background(MaterialTheme.colorScheme.surface)
            .border(1.dp, MaterialTheme.colorScheme.outlineVariant, RoundedCornerShape(10.dp)),
    ) {
        SegItem("本地", mode == "local") { onChange("local") }
        SegItem("云端", mode == "cloud") { onChange("cloud") }
    }
}

@Composable
private fun SegItem(label: String, selected: Boolean, onClick: () -> Unit) {
    Text(
        text = label,
        style = MaterialTheme.typography.labelLarge,
        color = if (selected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.onSurfaceVariant,
        textAlign = TextAlign.Center,
        modifier = Modifier
            .clickable(onClick = onClick)
            .background(if (selected) MaterialTheme.colorScheme.primary.copy(alpha = 0.15f) else Color.Transparent)
            .padding(horizontal = 14.dp, vertical = 8.dp),
    )
}

/** 点按即设进行中: 瓦片整体压暗遮罩 + 转圈 + 阶段文案(下载中/正在换上…)。 */
@Composable
private fun ApplyOverlay(show: Boolean, msg: String?) {
    if (!show) return
    Box(
        modifier = Modifier.fillMaxSize().background(Color.Black.copy(alpha = 0.5f)),
        contentAlignment = Alignment.Center,
    ) {
        Column(horizontalAlignment = Alignment.CenterHorizontally) {
            CircularProgressIndicator(Modifier.width(22.dp), color = Color.White, strokeWidth = 2.dp)
            Spacer(Modifier.height(6.dp))
            Text(msg ?: "…", color = Color.White, style = MaterialTheme.typography.bodySmall)
        }
    }
}

/** 点按即设成功: 瓦片盖 ✔ 徽标, 1.2s 后淡出。 */
@Composable
private fun AppliedBadge() {
    Box(
        modifier = Modifier.fillMaxSize().background(Color.Black.copy(alpha = 0.45f)),
        contentAlignment = Alignment.Center,
    ) { Text("✔", color = Color.White, fontSize = 34.sp) }
}

/** 云端缩略图: 按 jobId 落磁盘缓存(翻回不重拉), 原图字节降采样解码省内存(A 方案, 流量优化留待 B)。 */
@Composable
private fun CloudThumb(session: Session, jobId: String, modifier: Modifier = Modifier) {
    val ctx = LocalContext.current
    var bmp by remember(jobId) { mutableStateOf<android.graphics.Bitmap?>(null) }
    var st by remember(jobId) { mutableStateOf(0) } // 0 加载中 1 成功 2 失败
    LaunchedEffect(jobId) {
        val dir = File(ctx.cacheDir, "hist").apply { mkdirs() }
        val f = File(dir, "$jobId.jpg")
        val bytes = if (f.exists()) f.readBytes() else {
            try {
                val b = Api.download(session.state.value.token, "/api/v1/history/$jobId/image")
                f.writeBytes(b)
                b
            } catch (_: Exception) {
                st = 2
                return@LaunchedEffect
            }
        }
        bmp = decodeSampled(bytes)
        st = if (bmp != null) 1 else 2
    }
    Box(modifier = modifier, contentAlignment = Alignment.Center) {
        when (st) {
            0 -> CircularProgressIndicator(Modifier.width(22.dp), strokeWidth = 2.dp)
            1 -> bmp?.let {
                Image(
                    bitmap = it.asImageBitmap(),
                    contentDescription = null,
                    contentScale = ContentScale.Crop,
                    modifier = Modifier.fillMaxSize(),
                )
            }
            else -> Text("加载失败", style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
        }
    }
}

/** 原图字节按最长边 ≈512 目标降采样(网格瓦片显示用)。 */
private fun decodeSampled(bytes: ByteArray, target: Int = 512): android.graphics.Bitmap? {
    val bounds = android.graphics.BitmapFactory.Options().apply { inJustDecodeBounds = true }
    android.graphics.BitmapFactory.decodeByteArray(bytes, 0, bytes.size, bounds)
    var sample = 1
    val maxDim = maxOf(bounds.outWidth, bounds.outHeight)
    while (maxDim / (sample * 2) >= target) sample *= 2
    return android.graphics.BitmapFactory.decodeByteArray(
        bytes, 0, bytes.size,
        android.graphics.BitmapFactory.Options().apply { inSampleSize = sample },
    )
}

/** RFC3339(2026-11-13T16:17:05+08:00) → "11-13 16:17"; 格式不符原样返回。 */
private fun shortTime(s: String): String =
    if (s.length >= 16 && s.indexOf('T') in 1..14) "${s.substring(5, 10)} ${s.substring(11, 16)}" else s
