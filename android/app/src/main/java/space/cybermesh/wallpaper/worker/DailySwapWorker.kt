package space.cybermesh.wallpaper.worker

import android.content.Context
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters
import space.cybermesh.wallpaper.net.RenderEngine
import space.cybermesh.wallpaper.net.Session
import space.cybermesh.wallpaper.net.friendlyError

/**
 * 定点自动换图(每天 08:30 / 14:00, AutoSchedule OneTime 自链; 不常驻不轮询, 耗电≈0)。
 * 无论成败都排下一个定点(失败静默记录, 下个定点再试; 不返回 retry 以免高频重试耗电)。
 * 成功记录时间与结果到会话偏好供 UI 展示。
 */
class DailySwapWorker(appContext: Context, params: WorkerParameters) :
    CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val session = Session(applicationContext)
        session.load()
        try {
            if (session.state.value.loggedIn) {
                RenderEngine.renderAndSet(applicationContext, session)
                session.recordAutoRun(System.currentTimeMillis(), "ok")
            }
        } catch (e: Exception) {
            session.recordAutoRun(System.currentTimeMillis(), "fail: " + friendlyError(e))
        }
        // 自链: 排下一个定点(08:30/14:00)——即使失败/未登录也排, 保持链条不断
        AutoSchedule.enqueueNext(applicationContext)
        return Result.success()
    }
}
