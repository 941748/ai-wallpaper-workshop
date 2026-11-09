# P2 桌面端 Wails 迁移设计(walk → Wails v2)

> 状态: 设计定稿(2026-11-09, 负责人选型: 原生 HTML/CSS/JS + 原地替换)。
> 范围: 本批一次做完——Wails 迁移 + 云端历史 5 点(见 ROADMAP P2 行)。

## 1. 选型(已拍板)

| 项 | 决定 | 理由 |
|---|---|---|
| 框架 | Wails v2.17(已装 CLI) | Go 业务层零改动, 只换 UI 壳; 单 exe 产出 |
| 前端 | **原生 HTML/CSS/JS, 无构建步骤** | 零 npm 依赖, 静态文件直接 embed 进 exe, 贴合"单文件免安装" |
| 落位 | **原地替换**: `internal/ui` walk 文件删除, git 历史可回溯 | 单轨维护, 无双 UI 并存期 |
| 视觉 | 对齐官网 cybermesh.space 设计语言(深色卡片/圆角/主色) | P2 立项目标 |

## 2. 架构

```
main.go
  ├─ --tick/--apply-update/--uninstall/--cleanup  无头模式, 完全不动(不经过 UI 层)
  └─ 无参数 → ui.Run()
                 └─ wails.Run(options)  ← 唯一改动点
                      ├─ Assets: //go:embed frontend(根目录, 随 main 包 embed)
                      ├─ Bind:  ui.App(service struct, Go↔JS 桥)
                      └─ 单实例: wails SingleInstanceLock(替代 walk 版)
frontend/                    ← 新增, 根目录
  index.html  app.css  app.js (+ 各页面模块 js)
internal/ui/                 ← 原地重写
  app.go       wails.Run 封装 + App service 定义
  bridge.go    Go↔JS 绑定方法(向导/设置/历史/出图/配对)
  (删除) wizard.go settings.go widgets.go theme.go sidebar.go frameless.go …
  (保留) wizard_logic.go 等无 walk 依赖的纯逻辑
```

- **业务层零改动**: `internal/{config,store,tick,desktop,cloud,prompt,signals,update,install}` 原样复用; `service.go` 去 walk 化(makeBackend/ensureRegistered/testConnection 等纯函数保留)。
- **无头模式隔离**: 计划任务 `--tick` 与自更新/卸载助手路径不链接 wails 运行时窗口, 行为与现状一致。
- **卸载**: 设置页"卸载"按钮 → JS 确认框 → Go 方法拉起 `--cleanup` 助手并退出(替代 walk 对话框); `--uninstall` 命令行入口保留, 打开同一 App 的确认路由。

## 3. 关键风险与对策

| 风险 | 对策 |
|---|---|
| WebView2 缺失(Win10 老机) | wails 自带引导弹窗(给下载链接); 发版前在无 WebView2 环境实测; README 注明运行要求 |
| 单 exe + manifest/图标嵌入 | build.ps1 改造: `wails build` 产物 → 沿用 rsrc 注入 manifest/syso, 保持自更新与版本展示链路 |
| 自更新换装 | `--apply-update` 路径不动; 新 exe 仍是单文件 |
| walk 自绘控件(筹码/图表) | 前端用 CSS Grid/Flex + canvas/ECharts(官网已用, 可直接复用其 js) |

## 4. 云端历史 5 点(并入本批, 在新 UI 实现)

1. 云端列表行 = ♥ + 时间 + 规格, 不显示提示词(选中后预览下方展示全文)
2. 预览修复: 解码改 `image.Decode`(自动识别 PNG/JPEG)——根因是 JPEG 图喂给 `png.Decode`
3. 云端视觉区分: ☁ 行前缀 + 状态条 + 按钮文案「召回到本地并应用」+ 切换后自动预览首张
4. 全部召回: 留档命名 `{jobID}.png`(无前缀), 去重= os.Stat, 旧时间戳图不兼容(首次可重复), 进度 x/N, 服务端 `/history` 加 `?offset=` 分页
5. 安卓: 已配对状态也显示「生成配对码」(身份迁移); 随本批出安卓版

## 5. 里程碑

- **P2-a** 设计(本文件) ✅
- **P2-b** 骨架: wails 工程跑通, 主窗口 + 空页面 + Bind 通道
- **P2-c** 页面迁移: 向导 → 设置各 Tab → 对话框, walk 文件删除
- **P2-d** 云端历史 5 点 + 服务端 offset 分页
- **P2-e** 测试全绿 + build.ps1 改造 + 双端发版(等负责人指令)
