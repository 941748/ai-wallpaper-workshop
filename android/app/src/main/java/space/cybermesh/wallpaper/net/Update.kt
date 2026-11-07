package space.cybermesh.wallpaper.net

import android.content.Context
import android.content.Intent
import androidx.core.content.FileProvider
import java.io.File

/** APK 更新: 检查→下载(走 /go/apk 计数)→系统安装器。 */
object Update {

    /**
     * 检查并预下载最新 APK; 有新版返回 (version, 文件), 已最新或失败返回 null。
     * 调用方可放后台协程, 不阻塞主流程; 失败静默(更新检查不该影响正常功能)。
     */
    suspend fun fetchIfUpdate(ctx: Context, currentVersion: String): Pair<String, File>? {
        val latest = Api.androidLatest(currentVersion)
        if (latest.url.isEmpty()) return null
        val bytes = Api.download("", latest.url)
        val dir = File(ctx.filesDir, "apk").apply { mkdirs() }
        val f = File(dir, "AIWallpaper-${latest.version}.apk")
        f.writeBytes(bytes)
        return latest.version to f
    }

    /** 触发系统安装器(FileProvider 授权; 需要用户在系统弹窗确认)。 */
    fun install(ctx: Context, file: File) {
        val uri = FileProvider.getUriForFile(ctx, "${ctx.packageName}.fileprovider", file)
        val intent = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, "application/vnd.android.package-archive")
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        ctx.startActivity(intent)
    }
}
