package space.cybermesh.wallpaper.net

import android.content.Context
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.launch

/**
 * 进程级出图流程状态与执行器。
 * 画廊页的 remember 状态与 rememberCoroutineScope 会随页面切换销毁——
 * 进度显示消失、出图/下载协程被静默取消(服务端还在画, 本地却停了)。
 * 提升到进程级后: 切页期间进度持续、下载不中断, 返回画廊时进度原样恢复。
 */
object RenderFlow {
    private val _state = MutableStateFlow<RenderState>(RenderState.Idle)
    val state: StateFlow<RenderState> = _state

    private val _quotaMsg = MutableStateFlow<String?>(null)
    val quotaMsg: StateFlow<String?> = _quotaMsg

    // 进程级作用域: 不随 Composable 销毁(仅进程退出才取消)
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main)

    fun setQuota(msg: String?) {
        _quotaMsg.value = msg
    }

    private fun busy(): Boolean {
        val s = _state.value
        return s !is RenderState.Idle && s !is RenderState.Done && s !is RenderState.Error
    }

    /**
     * 换一张: 有未下载成功的任务就直接续(不限时——已出的图绝不丢), 否则新出图(此时才查配额)。
     * 续传分三态: done/排队中→补轮询下载; failed→自动废弃转新出图;
     * 任务行已被 7 天清理(404)→改走历史端点取回归档图(仍不烧配额), 历史也无才转新出图。
     */
    fun swap(ctx: Context, session: Session) {
        if (busy()) return
        scope.launch {
            _quotaMsg.value = null
            try {
                val pending = session.lastJob()
                val parts = pending.split("|")
                val now = System.currentTimeMillis()
                var resumeId = if (parts.size >= 2 && parts[0].isNotEmpty()) parts[0] else ""
                if (resumeId.isNotEmpty()) {
                    val probe = runCatching { Api.job(session.state.value.token, resumeId) }
                    val st = probe.getOrNull()
                    when {
                        st != null && st.status == "failed" -> {
                            session.setLastJob("")
                            resumeId = ""
                        }
                        st == null && probe.exceptionOrNull()?.message?.contains("任务不存在") == true -> {
                            // 任务行已清理但归档图仍在 → 历史端点取回(已出的图不丢)
                            try {
                                RenderEngine.finishFromHistory(ctx, session, resumeId) { _state.value = it }
                                session.setLastJob("")
                                return@launch
                            } catch (e: Exception) {
                                if (e.message?.contains("404") == true) {
                                    session.setLastJob("") // 历史也没有(已删除) → 转新出图
                                    resumeId = ""
                                } else {
                                    _state.value = RenderState.Error(friendlyError(e)) // 网络抖动 → 下次再试
                                    return@launch
                                }
                            }
                        }
                        // 网络抖动查不到 → 保留续传机会, 走原续传流程重试
                    }
                }
                val jobId = if (resumeId.isNotEmpty()) {
                    _state.value = RenderState.Rendering(0)
                    resumeId
                } else {
                    val id = RenderEngine.startJob(ctx, session) { _state.value = it }
                    session.setLastJob("$id|$now")
                    id
                }
                RenderEngine.finishJob(ctx, session, jobId) { _state.value = it }
                session.setLastJob("")
            } catch (e: Exception) {
                val msg = friendlyError(e)
                _state.value = RenderState.Error(msg)
                if (msg.contains("没画好") || msg.contains("找不到")) {
                    session.setLastJob("") // 任务本身失败 → 废弃, 下次全新出图
                }
            }
        }
    }
}
