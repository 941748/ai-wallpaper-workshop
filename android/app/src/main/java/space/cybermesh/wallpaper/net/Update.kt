package space.cybermesh.wallpaper.net

import android.content.Context
import android.content.Intent
import androidx.core.content.FileProvider
import java.io.File

/** APK 更新: 检查→下载(走 /go/apk 计数)→系统安装器。 */
object Update {

    /** 本会话是否已自动拉起过安装器(打开即查只自动弹一次, 避免反复打扰; 出图时弹条提醒不受此限)。 */
    @Volatile
    var autoPrompted = false

    /**
     * 检查并预下载最新 APK; 有新版返回 (version, 文件), 已最新或失败返回 null。
     * 调用方可放后台协程, 不阻塞主流程; 失败静默(更新检查不该影响正常功能)。
     * 同版本已下载过则免重下(上次用户取消安装的场景)。
     */
    suspend fun fetchIfUpdate(ctx: Context, currentVersion: String): Pair<String, File>? {
        val latest = Api.androidLatest(currentVersion)
        if (latest.url.isEmpty()) return null
        val dir = File(ctx.filesDir, "apk").apply { mkdirs() }
        val f = File(dir, "AIWallpaper-${latest.version}.apk")
        if (f.exists() && f.length() > 1_000_000) return latest.version to f
        val bytes = Api.download("", latest.url)
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
