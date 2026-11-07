package space.cybermesh.wallpaper.worker

import android.content.Context
import androidx.work.CoroutineWorker
import androidx.work.WorkerParameters
import space.cybermesh.wallpaper.net.RenderEngine
import space.cybermesh.wallpaper.net.Session
import space.cybermesh.wallpaper.net.friendlyError

/**
 * 每日自动换图(WorkManager 唯一周期任务, 间隔 1 天; 不常驻不轮询, 耗电≈0)。
 * 失败静默(下轮再试), 成功记录时间与结果到会话偏好供 UI 展示。
 */
class DailySwapWorker(appContext: Context, params: WorkerParameters) :
    CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        val session = Session(applicationContext)
        session.load()
        if (!session.state.value.loggedIn) return Result.success()
        return try {
            RenderEngine.renderAndSet(applicationContext, session)
            session.recordAutoRun(System.currentTimeMillis(), "ok")
            Result.success()
        } catch (e: Exception) {
            session.recordAutoRun(System.currentTimeMillis(), "fail: " + friendlyError(e))
            // 网络/配额类临时失败交给下一轮; 不返回 retry 以免高频重试耗电
            Result.success()
        }
    }
}
