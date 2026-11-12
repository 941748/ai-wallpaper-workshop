package space.cybermesh.wallpaper.net

import android.app.WallpaperManager
import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Point
import android.graphics.Rect
import android.graphics.RectF
import android.os.Build
import android.view.WindowManager
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
        setWallpaper(ctx, bmp)
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

    /**
     * 任务行已被 7 天清理但归档图仍在: 从历史端点取回(不烧配额)。
     * 出图机已出、已上传的图绝不因任务过期而丢弃。
     */
    suspend fun finishFromHistory(
        ctx: Context,
        session: Session,
        jobId: String,
        onState: (RenderState) -> Unit = {},
    ): File {
        val sess = session.state.value
        if (!sess.loggedIn) throw ApiException("未连接云端")
        onState(RenderState.Downloading)
        val bytes = Api.download(sess.token, "/api/v1/history/$jobId/image")
        val dir = File(ctx.filesDir, "wallpapers").apply { mkdirs() }
        val file = File(dir, "wp-$jobId.jpg")
        file.writeBytes(bytes)
        onState(RenderState.Applying)
        val bmp = BitmapFactory.decodeByteArray(bytes, 0, bytes.size)
            ?: throw ApiException("图片解码失败")
        setWallpaper(ctx, bmp)
        bmp.recycle()
        onState(RenderState.Done(file))
        return file
    }

    /** 设置一张已存在的本地图片为壁纸。 */
    fun setFile(ctx: Context, file: File) {
        val bmp = BitmapFactory.decodeFile(file.absolutePath)
            ?: throw ApiException("图片解码失败")
        setWallpaper(ctx, bmp)
        bmp.recycle()
    }

    /**
     * 设置壁纸统一入口(出图/历史重设/本地文件三处共用)。
     * 华为系(EMUI/鸿蒙): 先把图 fit 居中画进正方形缓冲(边=屏长边, 两侧黑边)再入库,
     * 与系统相册路径同形态——否则 EMUI 按错误映射把图放大 2.167 倍裁切(P30 实测,
     * hint=null 与整图 hint 均无效); 其他机型原图直设(整图 hint), 维持既有正常行为。
     */
    private fun setWallpaper(ctx: Context, bmp: Bitmap) {
        val wm = WallpaperManager.getInstance(ctx)
        val flags = WallpaperManager.FLAG_SYSTEM or WallpaperManager.FLAG_LOCK
        if (Build.MANUFACTURER.equals("HUAWEI", true) || Build.MANUFACTURER.equals("HONOR", true)) {
            val real = Point()
            (ctx.getSystemService(Context.WINDOW_SERVICE) as WindowManager).defaultDisplay.getRealSize(real)
            val side = maxOf(real.x, real.y)
            val out = Bitmap.createBitmap(side, side, Bitmap.Config.ARGB_8888)
            val canvas = Canvas(out)
            canvas.drawColor(Color.BLACK)
            val scale = minOf(side.toFloat() / bmp.width, side.toFloat() / bmp.height)
            val w = bmp.width * scale
            val h = bmp.height * scale
            val left = (side - w) / 2f
            val top = (side - h) / 2f
            canvas.drawBitmap(bmp, null, RectF(left, top, left + w, top + h), Paint(Paint.FILTER_BITMAP_FLAG))
            wm.setBitmap(out, Rect(0, 0, side, side), true, flags)
            out.recycle()
        } else {
            wm.setBitmap(bmp, Rect(0, 0, bmp.width, bmp.height), true, flags)
        }
    }

    /** 最近生成的壁纸(新→旧; 新命名携带 jobId 无时间序, 统一按修改时间排)。全量返回, 不设上限。 */
    fun recent(ctx: Context): List<File> {
        val dir = File(ctx.filesDir, "wallpapers")
        return dir.listFiles { f -> f.extension.equals("jpg", true) }
            ?.sortedByDescending { it.lastModified() }
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
