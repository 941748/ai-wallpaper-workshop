package space.cybermesh.wallpaper.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.TextButton
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import kotlinx.coroutines.launch
import space.cybermesh.wallpaper.net.Api
import space.cybermesh.wallpaper.net.ProfileResp
import space.cybermesh.wallpaper.net.ProfileSaveReq
import space.cybermesh.wallpaper.net.Session

enum class Tri { NONE, LIKE, DISLIKE }

/**
 * 偏好编辑器: 状态提升到导航层(切 tab 不丢), 显式点「同步到电脑」才写服务端。
 * 三态语义与 PC 一致: 喜欢0.9 / 点踩0.05 / 未选=移除键。
 */
class PrefEditor {
    var loaded by mutableStateOf<ProfileResp?>(null)
    var loadToken by mutableStateOf("")
    var profile by mutableStateOf<Map<String, Float>>(emptyMap())
    var disliked by mutableStateOf<Set<String>>(emptySet())
    var custom by mutableStateOf<List<String>>(emptyList())
    var packSel by mutableStateOf<Set<String>>(emptySet())
    var packsTouched by mutableStateOf(false)
    var dirty by mutableStateOf(false)
    var syncing by mutableStateOf(false)
    var msg by mutableStateOf<String?>(null)
    var err by mutableStateOf<String?>(null)

    fun load(token: String, p: ProfileResp) {
        loadToken = token
        loaded = p
        profile = p.profile
        disliked = p.disliked.toSet()
        custom = p.custom
        packSel = p.packs?.toSet() ?: emptySet()
        packsTouched = false
        dirty = false
    }

    fun stateOf(key: String): Tri = when {
        key in disliked -> Tri.DISLIKE
        (profile[key] ?: 0f) >= 0.89f -> Tri.LIKE
        else -> Tri.NONE
    }

    fun cycle(key: String) {
        dirty = true
        err = null
        msg = null
        when (stateOf(key)) {
            Tri.NONE -> {
                profile = profile + (key to 0.9f)
                disliked = disliked - key
            }
            Tri.LIKE -> {
                profile = profile + (key to 0.05f)
                disliked = disliked + key
            }
            Tri.DISLIKE -> {
                profile = profile - key
                disliked = disliked - key
            }
        }
    }

    fun togglePack(name: String) {
        dirty = true
        packsTouched = true
        err = null
        msg = null
        packSel = if (name in packSel) packSel - name else packSel + name
    }

    val hasLike: Boolean get() = profile.values.any { it >= 0.89f }

    /** 重置: 风格包与五维偏好全回未选(与 PC resetAllPreferences 同语义), 自定义关键词保留。
     *  dirty 置真——重新点选后经「同步到电脑」生效(服务端同规则拒绝全空推送, 防清空乱出图)。 */
    fun reset() {
        dirty = true
        packsTouched = true // 显式清空包: 重新点选后按当前选择同步
        err = null
        msg = "已重置为未选，重新点选后点「同步到电脑」双端生效"
        profile = emptyMap()
        disliked = emptySet()
        packSel = emptySet()
    }
}

/** 偏好(可编辑): 风格包多选 + 五维三态点选(灰→赞→踩→灰) + 同步到服务端双端共享。 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun PrefsScreen(session: Session, editor: PrefEditor) {
    val sess by session.state.collectAsState()
    val scope = rememberCoroutineScope()
    var confirmReset by remember { mutableStateOf(false) }

    LaunchedEffect(sess.token) {
        if (sess.token.isEmpty()) return@LaunchedEffect
        if (editor.loadToken != sess.token) {
            try {
                editor.load(sess.token, Api.profile(sess.token))
            } catch (e: Exception) {
                editor.err = e.message
            }
        }
    }

    val loaded = editor.loaded

    Column(
        modifier = Modifier
            .fillMaxSize()
            .verticalScroll(rememberScrollState())
            .padding(16.dp),
        verticalArrangement = Arrangement.spacedBy(10.dp),
    ) {
        Row {
            Text("我的偏好", style = MaterialTheme.typography.headlineSmall, modifier = Modifier.weight(1f))
            TextButton(
                enabled = !editor.syncing,
                onClick = { confirmReset = true },
            ) { Text("重置偏好", color = MaterialTheme.colorScheme.error) }
            Button(
                enabled = !editor.syncing && editor.dirty,
                onClick = {
                    scope.launch {
                        editor.syncing = true
                        editor.err = null
                        try {
                            if (editor.packSel.isEmpty() && !editor.hasLike) {
                                editor.err = "至少保留一个风格包或一个喜欢的标签"
                            } else {
                                val req = ProfileSaveReq(
                                    profile = editor.profile,
                                    disliked = editor.disliked.toList(),
                                    custom = editor.custom,
                                    packs = if (editor.packsTouched) editor.packSel.toList() else null,
                                )
                                Api.saveProfile(sess.token, req)
                                val fresh = Api.profile(sess.token)
                                editor.load(sess.token, fresh)
                                editor.msg = "已同步 ✓ 双端生效"
                            }
                        } catch (e: Exception) {
                            editor.err = e.message
                        }
                        editor.syncing = false
                    }
                },
            ) { Text(if (editor.syncing) "同步中…" else "同步到电脑") }
        }

        if (confirmReset) {
            AlertDialog(
                onDismissRequest = { confirmReset = false },
                title = { Text("重置偏好？") },
                text = {
                    Text(
                        "将清空风格包与五维偏好，喜欢/点踩全部回到未选，自定义关键词保留。\n" +
                            "重新点选后点「同步到电脑」即可双端生效。\n\n确定重置吗？"
                    )
                },
                confirmButton = {
                    TextButton(onClick = {
                        confirmReset = false
                        editor.reset()
                    }) { Text("重置", color = MaterialTheme.colorScheme.error) }
                },
                dismissButton = {
                    TextButton(onClick = { confirmReset = false }) { Text("取消") }
                },
            )
        }

        when {
            editor.err != null -> Text(
                editor.err!!,
                color = MaterialTheme.colorScheme.error,
                style = MaterialTheme.typography.bodySmall,
            )
            editor.msg != null -> Text(
                editor.msg!!,
                color = MaterialTheme.colorScheme.secondary,
                style = MaterialTheme.typography.bodySmall,
            )
            editor.dirty -> Text(
                "有未同步的修改 · 点右上角同步后电脑端立即生效",
                color = MaterialTheme.colorScheme.primary,
                style = MaterialTheme.typography.bodySmall,
            )
            else -> Text(
                "点选即可修改：灰→喜欢→点踩→取消 · 与电脑端共享同一份偏好",
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                style = MaterialTheme.typography.bodySmall,
            )
        }

        val dict = loaded?.dict
        when {
            loaded == null -> Text(
                if (editor.err == null) "加载中…" else "",
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            dict == null -> Text("词典加载失败，请下拉重进或稍后再试", color = MaterialTheme.colorScheme.error)
            else -> {
                // 选中包覆盖的词条(描边高亮)
                val covered = remember(editor.packSel, dict) {
                    dict.packs.filter { it.name in editor.packSel }
                        .flatMap { it.covers }
                        .toSet()
                }

                SectionTitle("风格包")
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    dict.packs.forEach { p ->
                        PackChip(
                            name = p.name,
                            selected = p.name in editor.packSel,
                            onClick = { editor.togglePack(p.name) },
                        )
                    }
                }
                if (!editor.packsTouched && loaded.packs == null) {
                    Text(
                        "未设置 · 按画像自动推断",
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                        style = MaterialTheme.typography.bodySmall,
                    )
                }

                dict.dimensions.forEach { dim ->
                    SectionTitle(dim.name)
                    FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                        dim.values.forEach { v ->
                            val key = "${dim.id}/${v.id}"
                            PrefChip(
                                text = v.name,
                                tri = editor.stateOf(key),
                                covered = key in covered,
                                onClick = { editor.cycle(key) },
                            )
                        }
                    }
                }

                Text(
                    "点踩优先级最高 · 包内词条带描边 · 至少保留一个包或一个喜欢",
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    style = MaterialTheme.typography.bodySmall,
                )
            }
        }
    }
}

@Composable
private fun SectionTitle(title: String) {
    Text(
        title,
        style = MaterialTheme.typography.titleSmall,
        color = MaterialTheme.colorScheme.primary,
        modifier = Modifier.padding(top = 6.dp),
    )
}

@Composable
private fun PackChip(name: String, selected: Boolean, onClick: () -> Unit) {
    Surface(
        shape = RoundedCornerShape(50),
        color = if (selected) MaterialTheme.colorScheme.primary else MaterialTheme.colorScheme.surfaceVariant,
        border = if (selected) BorderStroke(1.dp, MaterialTheme.colorScheme.primary) else null,
    ) {
        Text(
            (if (selected) "✓ " else "") + name,
            color = if (selected) MaterialTheme.colorScheme.onPrimary else MaterialTheme.colorScheme.onSurfaceVariant,
            fontSize = 13.sp,
            modifier = Modifier
                .clickable(onClick = onClick)
                .padding(horizontal = 12.dp, vertical = 7.dp),
        )
    }
}

@Composable
private fun PrefChip(text: String, tri: Tri, covered: Boolean, onClick: () -> Unit) {
    val scheme = MaterialTheme.colorScheme
    val bg = when (tri) {
        Tri.LIKE -> scheme.primary
        Tri.DISLIKE -> scheme.error
        Tri.NONE -> scheme.surfaceVariant
    }
    val fg = when (tri) {
        Tri.LIKE -> scheme.onPrimary
        Tri.DISLIKE -> Color.White
        Tri.NONE -> scheme.onSurfaceVariant
    }
    Surface(
        shape = RoundedCornerShape(50),
        color = bg,
        border = if (tri == Tri.NONE && covered) BorderStroke(1.5.dp, scheme.primary) else null,
    ) {
        Text(
            (if (tri == Tri.LIKE) "✓ " else "") + text,
            color = fg,
            fontSize = 13.sp,
            modifier = Modifier
                .clickable(onClick = onClick)
                .padding(horizontal = 12.dp, vertical = 7.dp),
        )
    }
}
