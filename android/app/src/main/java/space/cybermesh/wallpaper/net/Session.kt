package space.cybermesh.wallpaper.net

import android.content.Context
import android.provider.Settings
import android.os.Build
import android.util.DisplayMetrics
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import androidx.datastore.preferences.preferencesDataStore
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.first
import java.util.UUID

private val Context.dataStore by preferencesDataStore(name = "aw_session")
private val KEY_UID = stringPreferencesKey("user_id")
private val KEY_TOKEN = stringPreferencesKey("token")
private val KEY_DEVICE = stringPreferencesKey("device_id")
private val KEY_AUTO = stringPreferencesKey("auto_daily") // "1"=开启每日自动换
private val KEY_AUTO_LAST = stringPreferencesKey("auto_last") // "时间戳|结果"
private val KEY_LAST_JOB = stringPreferencesKey("last_job") // "jobId|失败时间戳" 断点续传
private val KEY_FAVS = stringPreferencesKey("favs") // 收藏 jobId 逗号分隔(心形打开即显缓存)

data class SessionData(
    val userId: String = "",
    val token: String = "",
    val deviceId: String = "",
) {
    val loggedIn: Boolean get() = token.isNotEmpty()
}

/** 会话: 匿名身份持久化 + 稳定设备 ID + device_info 构造(方案A: 注册即上报设备属性/屏幕)。 */
class Session(private val ctx: Context) {
    private val _state = MutableStateFlow(SessionData())
    val state: StateFlow<SessionData> = _state

    suspend fun load() {
        val prefs = ctx.dataStore.data.first()
        var device = prefs[KEY_DEVICE] ?: ""
        if (device.isEmpty()) {
            // P-设备: 稳定 ID(ANDROID_ID 同设备+同签名卸载重装不变)——根治重装重新配对后
            // devices 表多出重复设备行的问题(旧随机ID随 DataStore 消失, 新随机ID又插一行)。
            // 取不到时退回随机(个别 ROM 异常)。
            device = "android-" + (Settings.Secure.getString(
                ctx.contentResolver, Settings.Secure.ANDROID_ID
            )?.takeIf { it.length >= 8 } ?: UUID.randomUUID().toString().substring(0, 8))
            ctx.dataStore.edit { it[KEY_DEVICE] = device }
        }
        _state.value = SessionData(
            userId = prefs[KEY_UID] ?: "",
            token = prefs[KEY_TOKEN] ?: "",
            deviceId = device,
        )
    }

    suspend fun save(userId: String, token: String) {
        ctx.dataStore.edit {
            it[KEY_UID] = userId
            it[KEY_TOKEN] = token
        }
        _state.value = _state.value.copy(userId = userId, token = token)
    }

    /** 首启匿名注册(已有身份则跳过)。 */
    suspend fun ensureRegistered() {
        if (_state.value.loggedIn) return
        val resp = Api.register(_state.value.deviceId, deviceInfo())
        save(resp.userId, resp.token)
    }

    fun deviceInfo(): DeviceInfo {
        val dm: DisplayMetrics = ctx.resources.displayMetrics
        val version = try {
            ctx.packageManager.getPackageInfo(ctx.packageName, 0).versionName ?: "0"
        } catch (e: Exception) {
            "0"
        }
        return DeviceInfo(
            type = "android",
            model = Build.MODEL ?: "unknown",
            manufacturer = Build.MANUFACTURER ?: "",
            os = "Android",
            osVersion = Build.VERSION.RELEASE ?: "",
            appVersion = version,
            screenW = dm.widthPixels,
            screenH = dm.heightPixels,
        )
    }

    fun currentVersion(): String = deviceInfo().appVersion

    // ---------- 每日自动换偏好 ----------

    suspend fun autoEnabled(): Boolean = ctx.dataStore.data.first()[KEY_AUTO] == "1"

    suspend fun setAutoEnabled(on: Boolean) {
        ctx.dataStore.edit { it[KEY_AUTO] = if (on) "1" else "0" }
    }

    /** "时间戳|结果" 最近一次自动执行记录。 */
    suspend fun autoLast(): String = ctx.dataStore.data.first()[KEY_AUTO_LAST] ?: ""

    suspend fun recordAutoRun(atMillis: Long, result: String) {
        ctx.dataStore.edit { it[KEY_AUTO_LAST] = "$atMillis|$result" }
    }

    // ---------- 断点续传: 上次失败的出图任务 ----------

    suspend fun lastJob(): String = ctx.dataStore.data.first()[KEY_LAST_JOB] ?: ""

    suspend fun setLastJob(v: String) {
        ctx.dataStore.edit { it[KEY_LAST_JOB] = v }
    }

    // ---------- 收藏缓存(心形打开即显, 云端返回后纠偏) ----------

    suspend fun favs(): Set<String> =
        (ctx.dataStore.data.first()[KEY_FAVS] ?: "")
            .split(",").filter { it.isNotEmpty() }.toSet()

    suspend fun setFavs(ids: Set<String>) {
        ctx.dataStore.edit { it[KEY_FAVS] = ids.joinToString(",") }
    }
}
