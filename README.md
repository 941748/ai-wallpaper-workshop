# AI 壁纸工坊

> 每小时,桌面自己变好看。AI 为你一个人出图、自动换壁纸的 Windows 工具。

<p align="center">
  <img src="docs/screenshots/sample-grassland.jpg" width="49%" />
  <img src="docs/screenshots/sample-ink.jpg" width="49%" />
</p>
<p align="center"><sub>以上均为项目实际出图: 提示词由云端 LLM 结合用户画像与历史自动规划</sub></p>

**AI 壁纸工坊**不是一个壁纸图库,而是一台为你一个人工作的壁纸生成器:
Windows 计划任务每小时静默触发一次 → 云端 LLM 结合你的偏好画像与近期历史规划提示词 →
ComfyUI(Z-Image Turbo)出图 → 按屏幕物理分辨率适配 → 自动更换桌面壁纸。**你什么都不用做。**

零注册、零登录、无托盘、无常驻进程——关掉窗口,系统里就没有它的进程。

## 特性

- **无感偏好学习**: 五维问卷(风格 / 题材 / 色调 / 情绪 / 构图)+ 探针图点选建立画像;
  之后在你自然操作(重跑向导、重新应用历史壁纸、满意度回访)中检测偏好漂移, 静默微调画像。
- **每小时自动更新**: 计划任务触发秒级进程, 出图换图后退出, 无后台驻留。
- **云端 LLM 规划 + 静默降级**: LLM 结合画像、近 10 轮记录与漂移事件规划下一张;
  不可达/超时时自动回退本地加权采样引擎, 用户无感。
- **节日与节气语境**: 二十四节气与主要节日临近时, 画面氛围含蓄呼应(如母亲节以康乃馨色柔光致意), 只渲染氛围、不做文字横幅; 可一键关闭。
- **预生成池**: 出图机空闲时提前生成下一轮, 命中时换壁纸仅需十余秒(省去现场出图的约 30 秒)。
- **不打扰**: 安静时段、全屏应用、锁屏统统跳过; 满意度回访走记忆曲线(3→7→15→30→60→90 天封顶), 时点更少但永不消失。
- **隐私友好**: 匿名 token, 零注册; 画像与历史全部留在本机; 出站调用统一走你自托管的云服务。
- **轻**: 客户端为 ~10MB 单文件(Go 编译, 无运行时依赖)。

## 截图

五步向导: 连接服务 → 偏好问卷(五维三态筹码, 可一键推荐 / 探针校准) → 首张壁纸 → 注册每小时任务 → 完成。

<p align="center">
  <img src="docs/screenshots/wizard-connect.png" width="49%" />
  <img src="docs/screenshots/wizard-survey.png" width="49%" />
</p>

<p align="center">
  <img src="docs/screenshots/sample-lake.jpg" width="66%" />
</p>

## 工作原理

```
┌──────────────── Windows 客户端(单文件 ~10MB)────────────────┐
│  向导 / 设置(walk 自绘)          计划任务秒级进程 --tick        │
│  本地提示词引擎(LLM 不可达时兜底)  画像 / 历史 / 漂移信号       │
└─────────────────────────┬──────────────────────────────────┘
                  HTTPS(匿名 token, 仅出站)
┌─────────────────────────▼──────────────────────────────────┐
│  云服务(Go + SQLite 单二进制, 自托管)                         │
│  匿名注册 / 配额 / 限流          LLM 规划(默认小米 MiMo,       │
│  出图队列 normal / idle          可换任意 OpenAI 兼容服务)      │
│  预生成池 / 公共探针池 / 静默自更新分发                          │
└─────────────────────────┬──────────────────────────────────┘
                  内网 HTTP(出图机)
┌─────────────────────────▼──────────────────────────────────┐
│  ComfyUI(Z-Image Turbo, 消费级显卡 1080p 约 30 秒/张)          │
└────────────────────────────────────────────────────────────┘
```

### 偏好是怎么"学"到的

1. **问卷三态**: 五维 100+ 词条, 点选"喜欢 / 中立 / 讨厌", 映射为画像权重(0.9 / 0.3 / 0.05)。
2. **探针校准(可选)**: 12 张平衡组合探针图点选喜好, 推理出更精细的初始权重。
3. **漂移检测**: 只采纳真实用户行为(重跑向导、重新应用某张壁纸、调权重、回访选择),
   同向证据积累后微调画像; 不依赖"壁纸存活时长"之类的推测信号, 避免打扰与误判。

## 快速开始

### 方式一: 直连模式(单机自足, 适合已有显卡 + ComfyUI 的用户)

**不需要云服务**: 向导第 1 步勾选「直连局域网 ComfyUI」填地址即可, 出图与换壁纸完整可用
(提示词由本地引擎生成, 无需 LLM)。

1. 准备 ComfyUI 与模型(见下节「ComfyUI 准备清单」)。
2. 构建或下载客户端(见「构建」)。
3. 双击运行 → 向导勾选直连 → 填 `http://127.0.0.1:8188` → 走完向导。

### 方式二: 云端模式(推荐, 无显卡设备也能用)

1. 一台出图机: ComfyUI + 消费级 NVIDIA 显卡(Z-Image Turbo 1080p 约 30 秒/张)。
2. 一台服务器(可与出图机同机): 编译运行云服务(见「云服务部署」), 配置 LLM key。
3. 客户端向导第 1 步填云服务地址 → 完成。

## 构建

要求: Go 1.22+(windows/amd64)。依赖仅 `lxn/walk`、`golang.org/x/sys`、`golang.org/x/image`、
`golang.org/x/text`、`skip2/go-qrcode`、`modernc.org/sqlite`(纯 Go, 无 CGO)。

```powershell
# 一键构建: 跑测试 → dist\wallpaper.exe(GUI 无窗口) + dist\aiwallpaper-cloud.exe
powershell -ExecutionPolicy Bypass -File build.ps1

# 云服务部署到 Linux 服务器时, 构建 Linux 版二进制
$env:AW_BUILD_OS = "linux"; powershell -ExecutionPolicy Bypass -File build.ps1
```

日常开发:

```powershell
go test ./...     # 全部单元/集成测试(含跨模块端到端: 真实 tick ↔ 真实云服务 ↔ mock ComfyUI/LLM)
go vet ./...
go build -trimpath -ldflags "-s -w -H=windowsgui" -o dist\wallpaper.exe .
```

## 项目结构

```
wallpaper/
├── main.go                       # 无参=GUI(向导/设置);--tick=静默单次;--apply-update=换装助手
├── build.ps1                     # 一键构建(测试 → 客户端 → 云服务)
├── assets/workflow_template.json # Z-Image Turbo 工作流模板(仅注入提示词/尺寸/seed)
├── internal/
│   ├── config/                   # 配置与画像读写(%LOCALAPPDATA%\AIWallpaper)
│   ├── taxonomy/                 # 5 维属性词典(风格/题材/色调/情绪/构图)
│   ├── probe/                    # 探针平衡组合设计 + 点选→偏好画像推理
│   ├── prompt/                   # 本地提示词引擎(加权采样/探索/去重/负面词)
│   ├── backend/                  # ImageBackend 接口 + comfyui 直连 / runninghub 预留
│   ├── desktop/                  # 分辨率探测/缩放裁切/换壁纸/安静检测/低优先级
│   ├── scheduler/                # 计划任务 XML 生成与 schtasks 注册/删除/立即运行
│   ├── store/                    # 数据目录/文件锁/日志轮转/壁纸留档清理
│   ├── signals/                  # 漂移事件采集(重配置/重选壁纸/调权重/回访选择)
│   ├── cloud/                    # 云服务客户端(注册/取提示词/上报/补传队列/探针池)
│   ├── update/                   # 静默自更新(版本查询/下载校验/助手换装)
│   ├── tick/                     # --tick 全流程编排(含满意度回访与自更新)
│   └── ui/                       # walk 原生窗口: 向导/设置/回访小窗
├── cloud/                        # 独立云服务(单独编译部署)
│   ├── main.go                   # 装配 + 管理看板 /admin?token=
│   ├── api.go                    # /api/v1 全部接口
│   ├── comfy.go / queue.go       # ComfyUI 转发 + normal/idle 队列(空闲预生成)
│   ├── prompts.go                # LLM 规划 + 容错解析 + 本地引擎兜底
│   ├── workflows.go              # 出图工作流注册表(白名单)
│   ├── llm/client.go             # OpenAI 兼容 LLM 客户端(默认小米 MiMo)
│   └── store/                    # SQLite: users/prompt_records/signals/jobs/pregen/...
└── docs/screenshots/             # README 截图
```

## 客户端使用

| 运行方式 | 行为 |
| --- | --- |
| 双击运行(无参数) | 首次 → 初始化向导; 之后 → 设置面板。**关闭窗口 = 进程立即退出** |
| `wallpaper.exe --tick` | 计划任务每小时触发: 无窗口静默出图换壁纸后退出 |
| `wallpaper.exe --apply-update ...` | 内部换装助手(静默自更新用, 普通用户无需关心) |

设置面板页签: 偏好(权重调节/重新初始化) | 服务与调度(地址/测试连接/间隔/任务注册与删除/立即换一张) |
历史(缩略图/重新应用/打开目录) | 同步(匿名 user_id/扫码配对新设备) | 更新与回访(版本/更新检查/回访周期)。

以上手工操作(重跑向导重选、历史页重新应用、调权重)均本地记为漂移事件并上报云端,
作为 LLM 生成下一轮提示词的依据。

数据目录 `%LOCALAPPDATA%\AIWallpaper\`:

```
config.json          主配置(云端地址/相位/安静时段/满意度周期)
profile.json         偏好画像(5 维权重 + 负面片段)
prompt_history.json  最近 200 条出图组合(近 30 条去重)
cloud_queue.json     离线补传队列(最多重试 7 天)
signals.json         待上报的漂移事件
probes\ wallpapers\  探针缓存 / 壁纸留档(保留最近 30 张, 排除当前使用中)
bin\wallpaper.exe    向导注册计划任务时复制的正式副本
logs\runtime.log     运行日志(1MB × 3 份轮转)
```

卸载: 设置页 → 服务与调度 → 删除计划任务, 然后删除上述目录即可。

## ComfyUI 准备清单

云服务通过 HTTP API 转发出图, ComfyUI 侧只需:

1. ComfyUI 启动时监听可被云服务访问的地址(内网/隧道), 例如 `--listen 0.0.0.0 --port 8188`;
   若有反向代理或鉴权, 设置 `AW_COMFY_TOKEN` 为 Bearer 令牌。
2. 安装 Z-Image Turbo 工作流所需模型(出图工作流固定为 `z_image_wallpaper`, 仅注入提示词/尺寸/seed,
   其余参数冻结在模板内):

   - `models/diffusion_models/z_image_turbo_int8_convrot.safetensors`(UNet)
   - `models/text_encoders/qwen_3_4b_fp4_mixed.safetensors`(CLIP, lumina2)
   - `models/vae/ae.safetensors`(VAE)

3. 追加更多工作流(可选): 编辑 `clouddata/workflows.json`(首次启动自动生成)。
   模型与采样参数已固定在工作流模板内, 注册表仅约束出图尺寸范围(min_side/max_side):

   ```json
   [
     {"id":"wf-base","name":"Z-Image Turbo 通用","checkpoint":"z_image_turbo_int8_convrot.safetensors",
      "steps":8,"cfg":1,"sampler_name":"res_multistep","scheduler":"simple","min_side":512,"max_side":1920},
     {"id":"wf-small","name":"小尺寸档","checkpoint":"z_image_turbo_int8_convrot.safetensors",
      "steps":8,"cfg":1,"sampler_name":"res_multistep","scheduler":"simple","min_side":512,"max_side":1088}
   ]
   ```

   保存后重启云服务生效; LLM 仅在白名单内选择工作流, 未知 ID 自动回退 `wf-base`。
   客户端无需任何改动。默认出图尺寸为 1920×1080。
4. 无需安装 ComfyUI 节点插件: 云服务内置 Z-Image Turbo 标准工作流(CLIP 编码 →
   AuraFlow 采样 → KSampler → VAEDecode → SaveImage), 只要求上述标准节点与模型存在。

## 云服务部署

### 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `AW_ADDR` | `:8707` | 监听地址 |
| `AW_DATA` | `./clouddata` | 数据目录(db/探针池/成品图/预生成/更新包) |
| `AW_LLM_BASE` | `https://token-plan-cn.xiaomimimo.com/v1` | OpenAI 兼容 LLM 地址(默认小米 MiMo, 可换任意兼容服务) |
| `AW_LLM_KEY` | 空 | LLM 密钥(**仅存服务端**; 留空则提示词回退本地引擎, 仍可出图) |
| `AW_LLM_MODEL` | `mimo-v2.6-flash` | 模型名 |
| `AW_COMFY_BASE` | `http://127.0.0.1:8188` | 出图机 ComfyUI 地址(内网) |
| `AW_COMFY_TOKEN` | 空 | 访问 ComfyUI 的 Bearer 令牌(可选) |
| `AW_ADMIN_TOKEN` | 空 | 管理看板口令; 为空时看板关闭(404) |
| `AW_QUOTA_GEN_HOUR` / `AW_QUOTA_GEN_DAY` | `6` / `40` | 每用户出图配额(超出返回 429) |
| `AW_QUOTA_LLM_HOUR` / `AW_QUOTA_LLM_DAY` | `8` / `60` | 每用户 LLM 调用配额 |

### systemd 部署

```ini
# /etc/systemd/system/aiwallpaper-cloud.service
[Unit]
Description=AI Wallpaper Cloud
After=network.target

[Service]
User=aiwallpaper
WorkingDirectory=/opt/aiwallpaper
Environment=AW_ADDR=127.0.0.1:8707
Environment=AW_DATA=/var/lib/aiwallpaper
Environment=AW_LLM_KEY=<你的 LLM 密钥>
Environment=AW_COMFY_BASE=http://10.0.0.8:8188
Environment=AW_ADMIN_TOKEN=change-me
ExecStart=/opt/aiwallpaper/aiwallpaper-cloud
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload && sudo systemctl enable --now aiwallpaper-cloud
```

### nginx 反向代理(可选, 生产环境建议 HTTPS)

```nginx
server {
    listen 443 ssl;
    server_name wallpaper.example.com;
    # ssl_certificate ...; ssl_certificate_key ...;

    client_max_body_size 32m;

    location / {
        proxy_pass http://127.0.0.1:8707;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 120s;
    }
}
```

> 提示: 云服务不需要 WebSocket; 客户端全链路为 HTTP 轮询, 反代无需额外配置。
> 配对中转页与更新包下载走同一域名(`/d/{code}`、`/releases/`)。

### 管理看板

浏览器访问 `https://<你的域名>/admin?token=<AW_ADMIN_TOKEN>`:
查看全部用户(今日 LLM/出图用量、禁用状态)、最近出图记录、漂移事件、任务队列状态。

## 客户端版本发布(静默自更新)

1. 构建新客户端, 版本号在代码中维护; 产物改名为 `wallpaper-<version>.exe`。
2. 计算 sha256 与大小, 连同发布说明写入 `clouddata/releases/manifest.json`:

   ```json
   {"version":"0.2.0","file":"wallpaper-0.2.0.exe","sha256":"<64位hex>","size":10485760,
    "notes":"修复若干问题"}
   ```

3. 把 exe 放进 `clouddata/releases/`。**无需其他动作**: 每个 tick 尾部客户端会查询
   `/api/v1/client/latest`, 有新版本即静默下载 → sha256 校验 → 助手进程
   (`--apply-update`)在 tick 退出后原子换装, 下次 tick 生效。
   任一步失败保留旧版、记日志、下轮重试; 自更新位于换壁纸之后, 绝不中断主线。

## 数据与隐私

- 匿名 `user_id` + 每设备独立 token, 零注册零登录; 不采集任何个人身份信息。
- 云端存储: 按 user_id 的每轮提示词/属性/seed/成败、漂移事件与用量计数(用于偏好演化与配额)。
- 生成图仅在本机落盘(壁纸留档 30 张)与云端队列暂存(预生成, 取走后定期清理)。
- 跨设备同步: 旧设备设置页展示 6 位配对码(5 分钟有效, 一次性), 新设备输入即领回
  `user_id + token + 画像`。
- 更新包仅从自有云端 HTTPS 下载并做 sha256 校验, 不执行任何其他来源文件。

## FAQ

- **不联网能用吗?** 直连模式完全本地; 云端模式要求客户端能访问你的云服务。
- **出图速度与成本?** Z-Image Turbo 在 RTX 4060 上 1080p 约 30 秒/张;
  每小时一张的频率下, 一台消费级显卡可服务数十位用户。
- **支持哪些 LLM?** 任意 OpenAI 兼容接口(默认小米 MiMo); 不配置密钥时自动使用本地引擎, 功能不残缺。
- **为什么没有托盘/后台进程?** 这是设计红线: 任何时刻要么窗口开着, 要么系统里没有它。
- **支持 macOS / Linux 客户端吗?** 目前仅 Windows(调度承载于计划任务)。
- **内容安全?** 工作流与提示词自带负面词约束; 部署方可在云端按需增加审核环节。

## 开发与验收

- `go test ./...` 全绿(含跨模块端到端: 真实 tick ↔ 真实云服务 ↔ mock ComfyUI/LLM)
- 双击 `dist\wallpaper.exe` 进入向导; 走完后壁纸立即更换、计划任务注册成功
- 关窗后 `tasklist | findstr wallpaper` 无残留; `schtasks /Run /TN AIWallpaper` 静默换图、无窗口
- 断网运行: 保留旧壁纸、事件入补传队列, 恢复后自动补报
- 满意度回访到期弹窗: 满意 → 周期递增; 换个风格 → 立即换图并重置; 稍后 → 顺延 3 天

## License

[AGPL-3.0](https://www.gnu.org/licenses/agpl-3.0.html) — 你可以自由使用、修改、自托管;
若将修改后的版本作为网络服务提供给他人, 需以相同协议开源你的修改。
