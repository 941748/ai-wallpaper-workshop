package space.cybermesh.wallpaper.net

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.encodeToString
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.io.IOException
import java.net.URLEncoder
import java.util.concurrent.TimeUnit

class ApiException(msg: String) : IOException(msg)

@Serializable
data class Envelope<T>(val success: Boolean, val data: T? = null, val error: String? = null)

@Serializable
data class DeviceInfo(
    val type: String = "",
    val model: String = "",
    val os: String = "",
    @SerialName("os_version") val osVersion: String = "",
    @SerialName("app_version") val appVersion: String = "",
    @SerialName("screen_w") val screenW: Int = 0,
    @SerialName("screen_h") val screenH: Int = 0,
)

@Serializable
data class RegisterReq(
    val device: String,
    @SerialName("device_info") val deviceInfo: DeviceInfo? = null,
)

@Serializable
data class AuthResp(@SerialName("user_id") val userId: String, val token: String)

@Serializable
data class LinkCreateResp(val code: String, val url: String, @SerialName("expires_at") val expiresAt: String = "")

@Serializable
data class LinkReq(
    val action: String,
    val code: String = "",
    val device: String = "",
    @SerialName("device_info") val deviceInfo: DeviceInfo? = null,
)

@Serializable
data class ProfileResp(
    @SerialName("profile_version") val profileVersion: Int = 0,
    val profile: Map<String, Float> = emptyMap(),
    val disliked: List<String> = emptyList(),
    val custom: List<String> = emptyList(),
    val packs: List<String>? = null,
    val labels: Map<String, String> = emptyMap(),
    val paired: Boolean = false,
    val dict: Dict? = null,
)

@Serializable
data class DictVal(val id: String, val name: String)

@Serializable
data class DictDim(val id: String, val name: String, val values: List<DictVal> = emptyList())

@Serializable
data class DictPack(val name: String, val covers: List<String> = emptyList())

@Serializable
data class Dict(
    val dimensions: List<DictDim> = emptyList(),
    val packs: List<DictPack> = emptyList(),
)

@Serializable
data class ProfileSaveReq(
    val profile: Map<String, Float>,
    val disliked: List<String>? = null,
    val custom: List<String>? = null,
    val packs: List<String>? = null,
)

@Serializable
data class ProfileSaveResp(@SerialName("profile_version") val profileVersion: Int = 0)

@Serializable
data class NextReq(
    @SerialName("profile_version") val profileVersion: Int = 0,
    val profile: Map<String, Float> = emptyMap(),
    val disliked: List<String> = emptyList(),
    @SerialName("custom_keywords") val custom: List<String> = emptyList(),
    val packs: List<String>? = null,
    @SerialName("screen_w") val screenW: Int = 0,
    @SerialName("screen_h") val screenH: Int = 0,
    @SerialName("device_id") val deviceId: String = "",
)

@Serializable
data class NextResp(
    val positive: String,
    val negative: String,
    val width: Int,
    val height: Int,
    @SerialName("workflow_id") val workflowId: String = "",
    val combo: Map<String, String> = emptyMap(),
    val seed: Long = 0,
    val reason: String = "",
    val source: String = "",
)

@Serializable
data class GenParams(
    val positive: String,
    val negative: String,
    val width: Int,
    val height: Int,
    val seed: Long = 0,
    @SerialName("workflow_id") val workflowId: String = "",
    @SerialName("device_id") val deviceId: String = "",
)

@Serializable
data class JobIdResp(@SerialName("job_id") val jobId: String)

@Serializable
data class JobResp(val status: String, val error: String = "")

@Serializable
data class DeleteImageResp(val deleted: Boolean = false)

@Serializable
data class HealthResp(
    val ok: Boolean = false,
    @SerialName("quota_ok") val quotaOk: Boolean = true,
    @SerialName("render_ok") val renderOk: Boolean = true,
    @SerialName("server_version") val serverVersion: String = "",
)

@Serializable
data class AndroidLatestResp(
    val version: String,
    val url: String = "",
    val sha256: String = "",
    val size: Long = 0,
    val notes: String = "",
)

/** 云端 API 客户端: 统一 envelope 解析与 Bearer 鉴权; 调用均为挂起函数(IO 线程执行)。 */
object Api {
    const val BASE = "https://www.cybermesh.space"
    val json = Json { ignoreUnknownKeys = true; encodeDefaults = false }
    private val mediaJson = "application/json; charset=utf-8".toMediaType()
    private val client = OkHttpClient.Builder()
        .connectTimeout(15, TimeUnit.SECONDS)
        .readTimeout(120, TimeUnit.SECONDS)
        .build()

    private inline fun <reified T> call(method: String, path: String, token: String? = null, jsonBody: String? = null): T {
        val rb = Request.Builder().url(BASE + path)
        if (token != null) rb.header("Authorization", "Bearer $token")
        val body = jsonBody?.toRequestBody(mediaJson)
        rb.method(method, body)
        client.newCall(rb.build()).execute().use { resp ->
            val text = resp.body?.string() ?: ""
            val env = try {
                json.decodeFromString<Envelope<T>>(text)
            } catch (e: Exception) {
                throw IOException("HTTP ${resp.code}: ${text.take(160)}")
            }
            if (!env.success) throw ApiException(env.error ?: "HTTP ${resp.code}")
            return env.data ?: throw ApiException("空响应")
        }
    }

    suspend fun register(device: String, info: DeviceInfo?): AuthResp = withIO {
        call("POST", "/api/v1/users/register", null, json.encodeToString(RegisterReq(device, info)))
    }

    suspend fun linkCreate(token: String): LinkCreateResp = withIO {
        call("POST", "/api/v1/devices/link", token, json.encodeToString(LinkReq("create")))
    }

    suspend fun linkRedeem(code: String, device: String, info: DeviceInfo?): AuthResp = withIO {
        call("POST", "/api/v1/devices/link", null, json.encodeToString(LinkReq("redeem", code.trim(), device, info)))
    }

    suspend fun profile(token: String): ProfileResp = withIO {
        call("GET", "/api/v1/profile", token)
    }

    /** 推送编辑后的画像(服务端授版 pv), 返回新 profile_version。 */
    suspend fun saveProfile(token: String, req: ProfileSaveReq): Int = withIO {
        call<ProfileSaveResp>("POST", "/api/v1/profile", token, json.encodeToString(req)).profileVersion
    }

    suspend fun next(token: String, req: NextReq): NextResp = withIO {
        call("POST", "/api/v1/prompts/next", token, json.encodeToString(req))
    }

    suspend fun generate(token: String, p: GenParams): JobIdResp = withIO {
        call("POST", "/api/v1/generate", token, json.encodeToString(p))
    }

    suspend fun job(token: String, id: String): JobResp = withIO {
        call("GET", "/api/v1/generate/$id", token)
    }

    /** 删除云端图片副本(OSS 对象+服务端本地文件); 失败由调用方降级只删本地。 */
    suspend fun deleteImage(token: String, jobId: String) = withIO {
        call<DeleteImageResp>("POST", "/api/v1/images/delete", token, json.encodeToString(mapOf("job_id" to jobId)))
    }

    /** 下载字节(url 为 /api/v1 相对路径或完整 URL; token 非空时带 Bearer)。 */
    suspend fun download(token: String, path: String): ByteArray = withIO {
        val url = if (path.startsWith("http")) path else BASE + path
        val rb = Request.Builder().url(url)
        if (token.isNotEmpty()) rb.header("Authorization", "Bearer $token")
        client.newCall(rb.build()).execute().use { resp ->
            if (!resp.isSuccessful) throw IOException("下载 HTTP ${resp.code}")
            resp.body?.bytes() ?: throw IOException("空响应体")
        }
    }

    suspend fun health(): HealthResp = withIO { call("GET", "/api/v1/health") }

    suspend fun androidLatest(version: String): AndroidLatestResp = withIO {
        call("GET", "/api/v1/android/latest?version=${URLEncoder.encode(version, "UTF-8")}")
    }
}

private suspend fun <T> withIO(block: () -> T): T =
    kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { block() }
