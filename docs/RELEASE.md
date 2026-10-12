# 发布与双端同步操作手册

> 目标: 发版/推送**一次走完, 不返工、不反复试错**。全部命令在 Windows PowerShell 5.1、
> 仓库根目录执行(PS 5.1 不支持 `&&`, 用 `;`)。
> 本文所有服务端参数均为 2026-10 实测值; 变更时先更新本文再操作。

---

## 0. 硬性一致性纪律

1. **源码**: 每次推送, Gitee 与 GitHub 双仓的 `main` 分支与 `tag` 必须同步。
2. **发版四处一致**: Gitee Release(附件)、GitHub Release(附件)、官网(下载按钮/版本徽标/页脚)、
   服务端 `releases/manifest.json` —— 四处版本必须为同一最新版。
3. **版本号单一源**: `internal/version/version.go` 的 `Version`
   (客户端 `update.CurrentVersion` 与云服务 `serverVersion` 均引用它)。
   改这一处 + README 双语 + site/index.html + manifest 示例, 共四类落点。

## 1. 前置条件(一次性配置)

| 项 | 位置/方式 |
|---|---|
| Gitee token | `%USERPROFILE%\.aiwallpaper\gitee.token`(纯文本, **严禁入库**) |
| GitHub PAT | git credential manager, 发布脚本经 `git credential fill` 现取现用 |
| SSH 密钥 | `%USERPROFILE%\.ssh\id_ed25519_aiwallpaper` → `root@47.237.98.171:22`(免口令; orange 密钥有 passphrase, 批处理用不了) |
| 工具 | Go 1.22.x、python3 + `paramiko`(服务端部署)、`curl.exe`、rsrc(build.ps1 自动装) |
| PS5.1 约束 | `tools\*.ps1` 必须**纯 ASCII**; 中文文案放外部 UTF-8 文件用 `ReadAllText` 读取 |

服务端实测布局(2026-10):

| 项 | 值 |
|---|---|
| 主机 | `root@47.237.98.171`(SSH 直连; frp 只用于反代出图机/GitHub 推送兜底, **与 SSH 无关**) |
| systemd | `aiwallpaper-cloud`, 监听 `127.0.0.1:8707` |
| AW_DATA | `/var/lib/aiwallpaper`(`releases/` 放 exe 与 manifest.json; `cloud.db` 同级) |
| AW_SITE | `/opt/aiwallpaper/site`(官网 index.html) |
| AW_DL | `/opt/aiwallpaper/downloads`(主页 `/download/` 直连备用) |
| 云服务二进制 | `/opt/aiwallpaper/aiwallpaper-cloud`(历史 `.bak-*` 留档回滚) |

## 2. 日常源码推送(不发版)

```powershell
powershell -ExecutionPolicy Bypass -File tools\sync.ps1   # 推 main 到双端(Gitee token 直推; GitHub 直连失败自动 frp 兜底)
# tag 需单独推(sync.ps1 只推 main):
$tk = (Get-Content (Join-Path $env:USERPROFILE ".aiwallpaper\gitee.token") -Raw).Trim()
git push "https://oauth2:$tk@gitee.com/QQ941748/ai-wallpaper-workshop.git" <tag>
git push origin <tag>
```

## 3. 发版 checklist(每次新版本按序执行)

1. **全绿确认**: `go build ./... ; go vet ./... ; go test ./...`
   (或直接跑 `build.ps1`, 其第一步就是 `go test ./...`, 失败即停)
2. **升版本号**: `internal/version/version.go` → `const Version = "X.Y.Z"`
3. **构建**: `powershell -ExecutionPolicy Bypass -File build.ps1`
   → `dist\AIWallpaper.exe`(测试 + manifest/icon 嵌入 + GUI 链接一体)
4. **摘要与留档**:
   ```powershell
   (Get-FileHash -Algorithm SHA256 dist\AIWallpaper.exe).Hash.ToLower()
   (Get-Item dist\AIWallpaper.exe).Length
   Copy-Item -Force dist\AIWallpaper.exe dist\AIWallpaper-X.Y.Z.exe
   ```
5. **写三份发布文件**(均在 `dist/`, 不入库):
   - `manifest-X.Y.Z.json`: `version/file/sha256/size/notes/url`
     —— `url` 必填 Gitee 直链 `https://gitee.com/QQ941748/ai-wallpaper-workshop/releases/download/vX.Y.Z/AIWallpaper.exe`
     (字段语义见 README「客户端版本发布」)
   - `release-notes-X.Y.Z.md`(UTF-8)
   - `release-name-X.Y.Z.txt`(单行 Release 标题)
6. **复制发布脚本**: 以上一版为模板复制并全局替换版本号 →
   `tools\gitee-release-vX.Y.Z.ps1`、`tools\github-release-vX.Y.Z.ps1`
   (一次性脚本留档, **不改旧文件**)
7. **升三处展示**:
   - `README.md` / `README.en.md`: 版本徽标 + manifest 示例
   - `site/index.html`: 2 处下载按钮 href + 2 处版本徽标 + 页脚(搜旧版本号应归零)
8. **git 两段提交 + tag**:
   ```powershell
   git add <代码文件>; git commit -m "feat: vX.Y.Z 迭代——..."
   git add README.md README.en.md site/index.html tools/*vX.Y.Z*; git commit -m "chore: vX.Y.Z 发布——..."
   git tag -a vX.Y.Z -m "vX.Y.Z"
   ```
9. **双端推送**: `tools\sync.ps1` + 上一节的两条 tag 推送
10. **双端 Release**:
    ```powershell
    powershell -ExecutionPolicy Bypass -File tools\gitee-release-vX.Y.Z.ps1   # 输出 release id
    powershell -ExecutionPolicy Bypass -File tools\gitee-release-upload.ps1 -ReleaseId <id> -File dist\AIWallpaper.exe
    powershell -ExecutionPolicy Bypass -File tools\github-release-vX.Y.Z.ps1  # 建 Release + 传附件一体
    ```
    **附件文件名必须是 `AIWallpaper.exe`**(manifest 的 Gitee 直链依赖该文件名)
11. **服务端部署**(单连接一条命令):
    ```powershell
    # 版本号变更就必须重建 Linux 云服务(否则 /health 仍报旧 server_version):
    $env:GOOS="linux"; $env:GOARCH="amd64"
    go build -trimpath -ldflags "-s -w" -o dist\aiwallpaper-cloud ./cloud
    Remove-Item Env:GOOS,Env:GOARCH
    python tools/server-deploy.py --ver X.Y.Z --sha <64位hex>      # 文件+云服务全量
    # 可选: --files-only 仅发布文件; --cloud-only 仅云服务换装
    ```
    脚本内置: 60s 间隔限速重试、备份 `.bak-vX.Y.Z` + 失败自动回滚、远端 sha256 校验、
    迁移版本与 health 自检。manifest/官网按请求读文件, **上传即生效无需重启**; 仅云服务换装 restart。

## 4. 收尾核验(四处 + 自动更新链路)

```powershell
# (1) 官网: 旧版本号计 0, 新版本号计 5(2按钮+2徽标+页脚)
$s = curl.exe -sS https://www.cybermesh.space/; ([regex]::Matches($s,"vX\.Y\.Z")).Count
# (2) manifest 接口: 用旧版本号查询, 应返回新版本+Gitee url+正确 sha256/size
curl.exe -sS "https://www.cybermesh.space/api/v1/client/latest?version=<旧版>"
# (3)(4) 双端附件: 下载后 sha256 与本地一致(模拟客户端自更新校验)
curl.exe -sSL -o a.exe "https://gitee.com/QQ941748/ai-wallpaper-workshop/releases/download/vX.Y.Z/AIWallpaper.exe"
curl.exe -sSL -o b.exe "https://github.com/941748/ai-wallpaper-workshop/releases/download/vX.Y.Z/AIWallpaper.exe"
(Get-FileHash -Algorithm SHA256 a.exe).Hash.ToLower()   # 应等于本地 sha256
# (5) 云服务: /health 的 server_version 应为 X.Y.Z; cloud.db 的 schema_version 应到最新迁移号
curl.exe -sS "https://www.cybermesh.space/api/v1/health"
```

## 5. 坑集(必读, 均为实测教训)

1. **SSH 限速**: 这台机用 Windows OpenSSH 连接会间歇性 `EOFError` / KEX 阶段
   "Connection timed out"(banner 能读到, 握手被掐; 疑似对端对高频连接限速)。
   **必须用 `tools/server-deploy.py`(paramiko), 单连接完成全部操作, 脚本内置 60s×6 重试
   (实测第 2~5 次成功)。严禁 ssh/scp 多次反复连**(会加剧限速)。`IPQoS=none` 无效。
2. **discovery 不得整行打印 systemd Environment**(含 `AW_LLM_KEY`/`AW_ADMIN_TOKEN` 等机密,
   会进日志/会话)。server-deploy.py 只打印解析出的三个路径。
3. **PS5.1**: 中文 JSON body/commit message 走文件传递
   (`--data-binary @file` / 外部 UTF-8 文件), argv 直接传中文会 GBK 乱码;
   `.ps1` 文件本体保持纯 ASCII。
4. **Gitee 附件上传**必须带 `-H "Expect:"`(否则 100-continue 挂起), `--max-time 900`。
5. **tag 单独推**: `sync.ps1` 只推 main; tag 需按第 2 节命令分别推双端。
6. **云服务换装回滚**: `server-deploy.py` 自动备份 `.bak-vX.Y.Z`; 启动失败自动回滚重启。
   首次用带迁移的版本启动会在生产库重放 `schema_version` 迁移
   (全部幂等, 见 `cloud/store/store.go`; 回归测试 `TestPregenTableMigrationFromV1`)。
7. **gofmt 全仓 `-w` 会出现 LF→CRLF warning**(git autocrlf 正常现象);
   用 `git diff --ignore-all-space` 确认是否仅空白差异。
8. **历史文件勿"顺手统一"**: `tools\*-v旧版.ps1` 一次性脚本、
   `internal/install` 中"v1.0.4 及以前"注释均为有意保留。
9. **发布产物留档**: `dist\` 下历史 `AIWallpaper-*.exe`、`manifest-*.json`、
   `release-notes-*.md` 保留用于回滚与查阅(该目录不入库)。

## 6. 发布记录

| 日期 | 版本 | sha256(前12) | Gitee release id | 备注 |
|---|---|---|---|---|
| 2026-10-12 | v1.0.5 | e1cfe6d4de0e | 1199438 | 首次按本手册全流程; 云服务同版部署, 生产库迁移至 v4 |
