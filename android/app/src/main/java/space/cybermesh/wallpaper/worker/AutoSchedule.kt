package space.cybermesh.wallpaper.worker

import android.content.Context
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import java.time.Duration
import java.time.LocalDateTime
import java.util.concurrent.TimeUnit

/**
 * 定点自动换调度: 每天 **08:30 / 14:00** 两个定点, OneTime 任务自链——
 * 每次执行完按"下一个定点"计算初始延迟重新入队自己(替代旧 PeriodicWork(1天) 的
 * 开关联动时刻+24h 滚动, 负责人 2026-11-12 指令改定点)。
 * 失败也照排下个定点(不重试耗电); 迁移时取消旧周期任务, 防双跑。
 */
object AutoSchedule {
    const val WORK = "aw-auto-swap"
    const val LEGACY_WORK = "aw-daily-swap" // 旧周期任务(无定点), 升级迁移时取消

    private val slots = listOf(8 * 60 + 30, 14 * 60) // 08:30, 14:00 (分钟-of-day)

    /** 距下一个定点的延迟毫秒(本地时区; 今天两定点都已过则取明天 08:30)。 */
    fun nextDelayMillis(): Long {
        val nowLdt = LocalDateTime.now()
        val nowMin = nowLdt.hour * 60 + nowLdt.minute
        val targetMin = slots.firstOrNull { it > nowMin } ?: (slots[0] + 24 * 60)
        val deltaMin = targetMin - nowMin
        // 用分钟差粗对齐(秒级余量交给 WorkManager), 避免毫秒级漂移
        return TimeUnit.MINUTES.toMillis(deltaMin.toLong()) - (nowLdt.second * 1000L)
    }

    private fun constraints(): Constraints = Constraints.Builder()
        .setRequiredNetworkType(NetworkType.CONNECTED)
        .build()

    /** 排下一个定点(唯一任务, REPLACE 幂等重排)。 */
    fun enqueueNext(context: Context) {
        val req = OneTimeWorkRequestBuilder<DailySwapWorker>()
            .setInitialDelay(nextDelayMillis(), TimeUnit.MILLISECONDS)
            .setConstraints(constraints())
            .build()
        WorkManager.getInstance(context)
            .enqueueUniqueWork(WORK, ExistingWorkPolicy.REPLACE, req)
    }

    /** 升级迁移+启动: 取消旧周期任务(防双跑)并确保新链在排。幂等, 每次开 App 可调。 */
    fun migrate(context: Context) {
        val wm = WorkManager.getInstance(context)
        wm.cancelUniqueWork(LEGACY_WORK)
        enqueueNext(context)
    }

    /** 开关打开: 同 migrate。 */
    fun start(context: Context) = migrate(context)

    /** 开关关闭: 新旧任务全取消。 */
    fun stop(context: Context) {
        val wm = WorkManager.getInstance(context)
        wm.cancelUniqueWork(WORK)
        wm.cancelUniqueWork(LEGACY_WORK)
    }
}
