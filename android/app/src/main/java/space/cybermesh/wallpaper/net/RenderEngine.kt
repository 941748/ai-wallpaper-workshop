package space.cybermesh.wallpaper.net

import android.app.WallpaperManager
import android.content.Context
import android.graphics.BitmapFactory
import kotlinx.coroutines.delay
import java.io.File

/** 出图进度状态(供 UI 展示)。 */
sealed class RenderState {
    data object Idle : RenderState()
    data object Planning : RenderState()
    data class Rendering(val elapsedSec: Int) : RenderState()
    data object Downloading : RenderState()
    data object Applying : RenderState()
    data class Done(val file: File) : RenderState()
    data class Error(val msg: String) : RenderState()
}

/**
 * 出图链路分段(支持断点续传):
 *  - startJob: 画像+规划+下单, 返回 jobId(下单成功才返回 → 失败重试不会浪费已生成的图)
 *  - finishJob: 轮询→下载→落盘→设壁纸(网络闪断后可只重试这一段)
 *  - renderAndSet: 完整链路(每日自动换用)
 */
object RenderEngine {

    /** 阶段一: 规划并下单, 返回 jobId。 */
    suspend fun startJob(
        ctx: Context,
        session: Session,
        onState: (RenderState) -> Unit = {},
    ): String {
        val sess = session.state.value
        if (!sess.loggedIn) throw ApiException("未连接云端")

        onState(RenderState.Planning)
        val prof = Api.profile(sess.token)
        val dm = ctx.resources.displayMetrics
        val next = Api.next(sess.token, NextReq(
            profileVersion = prof.profileVersion,
            profile = prof.profile,
            disliked = prof.disliked,
            custom = prof.custom,
            packs = prof.packs,
            screenW = dm.widthPixels,
            screenH = dm.heightPixels,
            deviceId = sess.deviceId,
        ))
        val job = Api.generate(sess.token, GenParams(
            positive = next.positive,
            negative = next.negative,
            width = next.width,
            height = next.height,
            seed = next.seed,
            workflowId = next.workflowId,
            deviceId = sess.deviceId,
        ))
        return job.jobId
    }

    /** 阶段二: 轮询成品 → 下载 → 落盘 → 设置壁纸(桌面+锁屏)。 */
    suspend fun finishJob(
        ctx: Context,
        session: Session,
        jobId: String,
        onState: (RenderState) -> Unit = {},
    ): File {
        val sess = session.state.value
        if (!sess.loggedIn) throw ApiException("未连接云端")

        val startAt = System.currentTimeMillis()
        var ok = false
        run polling@{
            repeat(90) {
                delay(2000)
                onState(RenderState.Rendering(((System.currentTimeMillis() - startAt) / 1000).toInt()))
                val st = Api.job(sess.token, jobId)
                when (st.status) {
                    "done" -> {
                        ok = true
                        return@polling
                    }
                    "failed" -> throw ApiException("出图失败: " + st.error.ifEmpty { "未知原因" })
                }
            }
        }
        if (!ok) throw ApiException("出图超时, 请稍后再试")

        onState(RenderState.Downloading)
        val bytes = Api.download(sess.token, "/api/v1/generate/$jobId/image")

        val dir = File(ctx.filesDir, "wallpapers").apply { mkdirs() }
        // 文件名携带 jobId: 删除时可反查任务同步清理云端副本(旧文件为时间戳名, 只删本地)
        val file = File(dir, "wp-$jobId.jpg")
        file.writeBytes(bytes)

        onState(RenderState.Applying)
        val bmp = BitmapFactory.decodeByteArray(bytes, 0, bytes.size)
            ?: throw ApiException("图片解码失败")
        val wm = WallpaperManager.getInstance(ctx)
        wm.setBitmap(bmp, null, true, WallpaperManager.FLAG_SYSTEM or WallpaperManager.FLAG_LOCK)
        bmp.recycle()

        onState(RenderState.Done(file))
        return file
    }

    /** 完整链路(每日自动换等一次性场景)。 */
    suspend fun renderAndSet(
        ctx: Context,
        session: Session,
        onState: (RenderState) -> Unit = {},
    ): File {
        val jobId = startJob(ctx, session, onState)
        return finishJob(ctx, session, jobId, onState)
    }

    /** 设置一张已存在的本地图片为壁纸。 */
    fun setFile(ctx: Context, file: File) {
        val bmp = BitmapFactory.decodeFile(file.absolutePath)
            ?: throw ApiException("图片解码失败")
        val wm = WallpaperManager.getInstance(ctx)
        wm.setBitmap(bmp, null, true, WallpaperManager.FLAG_SYSTEM or WallpaperManager.FLAG_LOCK)
        bmp.recycle()
    }

    /** 最近生成的壁纸(新→旧; 新命名携带 jobId 无时间序, 统一按修改时间排)。 */
    fun recent(ctx: Context, limit: Int = 12): List<File> {
        val dir = File(ctx.filesDir, "wallpapers")
        return dir.listFiles { f -> f.extension.equals("jpg", true) }
            ?.sortedByDescending { it.lastModified() }
            ?.take(limit)
            ?: emptyList()
    }

    /** 当前系统壁纸(可能由其他设备设置)。SDK 无 getBitmap, 由 Drawable 绘制转换。 */
    fun currentWallpaper(ctx: Context): android.graphics.Bitmap? {
        return try {
            val d = WallpaperManager.getInstance(ctx).drawable ?: return null
            val w = d.intrinsicWidth.takeIf { it > 0 } ?: ctx.resources.displayMetrics.widthPixels
            val h = d.intrinsicHeight.takeIf { it > 0 } ?: ctx.resources.displayMetrics.heightPixels
            val bmp = android.graphics.Bitmap.createBitmap(w, h, android.graphics.Bitmap.Config.ARGB_8888)
            val canvas = android.graphics.Canvas(bmp)
            d.setBounds(0, 0, w, h)
            d.draw(canvas)
            bmp
        } catch (e: Exception) {
            null
        }
    }
}
