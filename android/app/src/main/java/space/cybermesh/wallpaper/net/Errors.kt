package space.cybermesh.wallpaper.net

import java.io.IOException
import java.net.ConnectException
import java.net.SocketTimeoutException
import java.net.UnknownHostException

/**
 * 报错委婉化: 按失败类型给可读、可重试引导的文案。
 * 规则: 传输类 → 温柔提示; 服务端业务错误多为中文可读 → 原样; 其余 → 通用兜底。
 */
fun friendlyError(e: Throwable): String {
    val msg = e.message ?: ""
    return when {
        e is ApiException && msg.contains("quota") ->
            "今日配额已用完，明天再来 ✨"
        e is ApiException && msg.contains("出图失败") ->
            "这次没画好，换一张试试吧"
        e is ApiException && msg.contains("任务不存在") ->
            "刚才那张找不到了，重新来一张吧"
        e is ApiException && msg.contains("超时") ->
            "云端画得有点慢，超时了… 再试一次"
        e is UnknownHostException || e is ConnectException ||
            msg.contains("Unable to resolve host", true) ||
            msg.contains("Failed to connect", true) ||
            msg.contains("Connection reset", true) ||
            msg.contains("Connection refused", true) ||
            msg.contains("closed connection", true) ->
            "信号迷路了… 检查下网络，再试一次"
        e is SocketTimeoutException || msg.contains("timeout", true) || msg.contains("timed out", true) ->
            "云端画得有点慢，超时了… 再试一次"
        e is IOException ->
            "网络开小差了… 再试一次"
        e is ApiException ->
            msg // 服务端业务提示(如"配对码无效或已过期")本身可读
        else ->
            "出了点小状况，再试一次吧"
    }
}
