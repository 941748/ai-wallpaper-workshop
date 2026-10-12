package ui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lxn/walk"

	"wallpaper/internal/backend"
	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/desktop"
	"wallpaper/internal/install"
	"wallpaper/internal/prompt"
	"wallpaper/internal/scheduler"
	"wallpaper/internal/signals"
	"wallpaper/internal/store"
	"wallpaper/internal/survey"
	"wallpaper/internal/update"
)

// wizard 初始化向导(4 步; 左侧自绘步骤导航, 允许返回已完成步骤)。
// 流程: 连接服务 → 偏好问卷(五维三态筹码) → 首张壁纸(喜欢/换一张, 进度可见) → 完成(静默安装+注册每小时计划任务)。
type wizard struct {
	mw  *walk.MainWindow
	cfg *config.Config
	dir string
	st  *store.Store
	cc  *cloud.Client
	be  backend.ImageBackend

	step       int
	maxReached int // 已解锁的最大步骤(导航门禁)
	titleLbl   *walk.Label
	hintLbl    *walk.Label
	content    *walk.Composite
	btnNext    *walk.PushButton
	btnExtra   *walk.PushButton
	btnCancel  *walk.PushButton
	statusLbl  *walk.Label
	progress   *walk.ProgressBar
	busy       bool
	stepDone   bool // 当前步骤前置条件是否完成(允许进入下一步)
	renderOK   bool // 出图链路是否可用(步骤 0 探测; 步骤 2 首张壁纸门禁)

	nav *navSidebar

	// 步骤 1: 偏好问卷(五维三态)
	surveyAnswers map[string]survey.Attitude // 勾选状态单一数据源(跨返回导航保持)
	lastSurvey    map[string]survey.Attitude // 最近一次已落盘快照(避免重复写入/重复信号)
	surveyDimIdx  int                        // 问卷当前维度标签(跨返回导航保持)
	chips         *surveyChips
	likesLbl      *walk.Label
	dislikesLbl   *walk.Label
	focusLbl      *walk.Label
	profile       *config.Profile

	// 步骤 2: 首张壁纸
	firstPreview   *walk.ImageView
	firstPreviewBm *walk.Bitmap // 返回重进时恢复预览
	firstOK        bool         // 当前壁纸已成功生成并应用
	firstAccepted  bool         // 是否已点过"喜欢"(避免重复信号)
	genSeq         int          // 生成序号(导航离开后使在途回调失效)
	firstTick      *time.Ticker // 首张壁纸生成进度刷新定时器(nil=未在生成)

	// 步骤 3: 完成(静默安装/注册结果)
	finalized     bool   // 是否已写过完成信号
	regOK         bool   // 计划任务/快捷方式是否安装成功
	installedPath string // 已安装 exe 路径(完成页展示)

	// 步骤 0: 连接设置控件
	directCheck   *walk.CheckBox
	directURLEdit *walk.LineEdit
	directTokEdit *walk.LineEdit
	connStatusLbl *walk.Label
}

var navTitles = []string{"连接服务", "偏好问卷", "首张壁纸", "完成"}

// RunWizard 打开初始化向导(阻塞直到窗口关闭)。
func RunWizard(dir string, st *store.Store, cfg *config.Config) {
	w := &wizard{
		cfg: cfg,
		dir: dir,
		st:  st,
	}
	if err := w.build(); err != nil {
		st.Log("wizard 窗口创建失败: %v", err)
		showError(nil, "窗口创建失败: %v", err)
		return
	}
	w.setStep(0)
	w.mw.Show()
	w.mw.Run()
}

func (w *wizard) build() error {
	initTheme()
	mw, err := walk.NewMainWindow()
	if err != nil {
		return err
	}
	w.mw = mw
	mw.SetTitle(config.AppNameCN + " — 初始化向导")
	// 标题栏/任务栏图标: 从 exe 资源加载(rsrc 嵌入的 RT_GROUP_ICON id=2; manifest 占用 id=1)
	if icon, err := walk.NewIconFromResourceId(2); err == nil {
		_ = mw.SetIcon(icon)
	}
	mw.SetSize(fitWorkArea(mw, walk.Size{Width: 1040, Height: 720}))
	mw.SetMinMaxSize(fitWorkArea(mw, walk.Size{Width: 900, Height: 620}), walk.Size{})
	_ = mw.SetLayout(walk.NewVBoxLayout())
	mw.SetBackground(brushPageBg)

	// 顶栏: 无边框标题栏(青绿渐变 + 产品名 + 拖动热区 + 右上角扁平 ✕)
	if _, err := newTitleBar(mw, mw, config.AppNameCN, "初始化向导", 52, func() { mw.Close() }); err != nil {
		return err
	}

	// 主体: 左导航 + 分隔线 + 右内容
	body, _ := walk.NewComposite(mw)
	_ = body.SetLayout(walk.NewHBoxLayout())
	navPane, _ := walk.NewComposite(body)
	_ = navPane.SetMinMaxSize(walk.Size{Width: 190}, walk.Size{Width: 190})
	navPane.SetBackground(brushNavBg)
	nv := walk.NewVBoxLayout()
	nv.SetSpacing(0)
	_ = navPane.SetLayout(nv)
	w.nav, err = newNavSidebar(navPane, 190, navTitles, w.navClick)
	if err != nil {
		return err
	}

	sep, _ := walk.NewComposite(body)
	_ = sep.SetMinMaxSize(walk.Size{Width: 1}, walk.Size{Width: 1})
	sep.SetBackground(brushSeparator)

	right, _ := walk.NewComposite(body)
	rv := walk.NewVBoxLayout()
	_ = rv.SetMargins(walk.Margins{HNear: 26, VNear: 18, HFar: 26, VFar: 10})
	rv.SetSpacing(6)
	_ = right.SetLayout(rv)
	right.SetBackground(brushWhite)
	w.titleLbl = themedLabel(right, "", themeFontStep, colTitle)
	w.hintLbl = themedLabel(right, "", nil, colHint)
	w.content, _ = walk.NewComposite(right)
	cv := walk.NewVBoxLayout()
	cv.SetSpacing(10)
	_ = w.content.SetLayout(cv)
	if bl, ok := right.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(w.content, 1)
	}

	// 分隔线 + 底部状态行/按钮行
	fsep, _ := walk.NewComposite(mw)
	_ = fsep.SetMinMaxSize(walk.Size{Height: 1}, walk.Size{Height: 1})
	fsep.SetBackground(brushSeparator)

	footer, _ := walk.NewComposite(mw)
	fv := walk.NewVBoxLayout()
	_ = fv.SetMargins(walk.Margins{HNear: 26, VNear: 8, HFar: 26, VFar: 14})
	fv.SetSpacing(6)
	_ = footer.SetLayout(fv)
	footer.SetBackground(brushWhite)

	statusRow, _ := walk.NewComposite(footer)
	_ = statusRow.SetLayout(walk.NewHBoxLayout())
	w.statusLbl = themedLabel(statusRow, "", nil, colHint)
	w.progress, _ = walk.NewProgressBar(statusRow)
	w.progress.SetRange(0, 100)
	w.progress.SetVisible(false)
	if bl, ok := statusRow.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(w.statusLbl, 1)
	}

	btnRow, _ := walk.NewComposite(footer)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	hsp, _ := walk.NewHSpacer(btnRow)
	w.btnCancel, _ = walk.NewPushButton(btnRow)
	w.btnCancel.SetText("退出")
	w.btnCancel.Clicked().Attach(func() { w.mw.Close() })
	w.btnNext, _ = walk.NewPushButton(btnRow)
	w.btnNext.SetText("下一步")
	w.btnNext.Clicked().Attach(w.onNext)
	w.btnExtra, _ = walk.NewPushButton(btnRow)
	w.btnExtra.SetVisible(false)
	w.btnExtra.Clicked().Attach(w.onExtra)
	if bl, ok := btnRow.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(hsp, 1)
	}
	if bl, ok := mw.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(body, 1) // 主体填满顶栏与底栏之间
	}
	enableCustomFrame(mw) // 无边框(保留阴影/贴靠/边缘拉伸)
	return nil
}

// navClick 左侧导航跳转: 仅允许返回已解锁步骤(探针校准中锁定)。
func (w *wizard) navClick(n int) {
	if n == w.step || n > w.maxReached {
		return
	}
	w.setStep(n)
}

// setStep 切换到指定步骤(重建内容区, 支持任意方向跳转)。
func (w *wizard) setStep(n int) {
	w.step = n
	if n > w.maxReached {
		w.maxReached = n
	}
	w.stepDone = false
	w.busy = false
	w.genSeq++ // 使上一页在途生成回调失效
	w.btnExtra.SetVisible(false)
	w.btnNext.SetEnabled(false)
	clearChildren(w.content)
	w.progress.SetVisible(false)
	w.progress.SetValue(0)
	w.setStatus("", colHint)
	w.nav.set(w.step, w.maxReached)

	switch n {
	case 0:
		w.titleLbl.SetText("第 1 步 / 共 4 步 · 连接出图服务")
		w.hintLbl.SetText("客户端所有出图与大模型调用都经云端代理, 用于统计与控制调用量; 也可直连局域网 ComfyUI 调试。")
		w.buildConnStep()
	case 1:
		w.titleLbl.SetText("第 2 步 / 共 4 步 · 勾选你的偏好")
		w.hintLbl.SetText("点圆角筹码循环切换: 中立 → ✓ 喜欢 → ✕ 讨厌; 顶部标签可切换维度。")
		w.buildSurveyStep()
	case 2:
		w.titleLbl.SetText("第 3 步 / 共 4 步 · 生成第一张壁纸")
		w.hintLbl.SetText("按你的屏幕分辨率出图并立即应用; 不满意点\"换一张\"。")
		w.buildFirstWallpaperStep()
	case 3:
		w.titleLbl.SetText("第 4 步 / 共 4 步 · 完成")
		w.hintLbl.SetText("")
		w.buildDoneStep()
	}
}

// setStatus 统一设置状态行文字与颜色。
func (w *wizard) setStatus(text string, color walk.Color) {
	w.statusLbl.SetText(text)
	w.statusLbl.SetTextColor(color)
}

// onNext 下一步按钮分发。
func (w *wizard) onNext() {
	if w.busy {
		return
	}
	switch w.step {
	case 0:
		if w.testAndRegister() {
			w.setStep(1)
		}
	case 1:
		w.nextFromSurvey()
	case 2:
		if w.stepDone {
			w.finishFirstWallpaper()
		}
	case 3:
		if !w.finalized {
			w.cfg.Initialized = true
			_ = w.cfg.Save(w.dir)
			_ = signals.Append(w.dir, signals.Event{Type: signals.TypeReinitialized, Detail: "初始化向导完成"})
			w.finalized = true
		}
		w.mw.Close()
	}
}

// onExtra 附加按钮分发(问卷校准入口 / 首张壁纸换一张)。
func (w *wizard) onExtra() {
	switch w.step {
	case 2:
		if w.stepDone && !w.busy {
			if w.firstOK {
				_ = signals.Append(w.dir, signals.Event{Type: signals.TypeStyleReject, Detail: "初始化首张壁纸: 换一张"})
				w.genFirstWallpaper(true) // 已有成品: 换个风格
				return
			}
			w.retryFirstWallpaper() // 尚无成品: 重探出图链路后重试/跳过
		}
	}
}

func (w *wizard) enableNext(text string) {
	w.btnNext.SetText(text)
	w.btnNext.SetEnabled(true)
}

// ---------- 步骤 0: 连接测试 ----------

func (w *wizard) buildConnStep() {
	cloudGroup, _ := walk.NewGroupBox(w.content)
	cloudGroup.SetTitle("云端服务")
	cloudGroup.SetBackground(brushWhite)
	cgl := walk.NewVBoxLayout()
	cgl.SetSpacing(8)
	_ = cloudGroup.SetLayout(cgl)
	themedLabel(cloudGroup, "客户端所有出图与大模型调用都经云端代理, 服务地址由官方云端提供、默认即可; 本地调试可在下方勾选直连模式。", nil, colHint)

	advGroup, _ := walk.NewGroupBox(w.content)
	advGroup.SetTitle("调试模式 (高级, 默认关闭)")
	advGroup.SetBackground(brushWhite)
	agl := walk.NewVBoxLayout()
	agl.SetSpacing(8)
	_ = advGroup.SetLayout(agl)
	w.directCheck, _ = walk.NewCheckBox(advGroup)
	w.directCheck.SetText("直连局域网 ComfyUI, 跳过云端代理与匿名注册")
	w.directCheck.SetChecked(w.cfg.DirectMode)

	directRow, _ := walk.NewComposite(advGroup)
	_ = directRow.SetLayout(walk.NewVBoxLayout())
	w.directURLEdit, _ = addLineRow(directRow, "ComfyUI 地址:", w.cfg.DirectURL, 420)
	w.directTokEdit, _ = addLineRow(directRow, "访问令牌(可选):", w.cfg.DirectToken, 420)
	directRow.SetVisible(w.cfg.DirectMode)
	w.directCheck.CheckedChanged().Attach(func() {
		directRow.SetVisible(w.directCheck.Checked())
	})

	testRow, _ := walk.NewComposite(w.content)
	_ = testRow.SetLayout(walk.NewHBoxLayout())
	testBtn, _ := walk.NewPushButton(testRow)
	testBtn.SetText("测试连接")
	w.connStatusLbl = themedLabel(testRow, "", nil, colHint)
	hsp, _ := walk.NewHSpacer(testRow)
	testBtn.Clicked().Attach(func() { w.runConnTest() })
	if bl, ok := testRow.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(hsp, 1)
	}
	w.enableNext("下一步")
}

func (w *wizard) runConnTest() {
	if w.busy {
		return
	}
	w.busy = true
	w.connStatusLbl.SetText("测试中...")
	w.connStatusLbl.SetTextColor(colHint)
	go func() {
		cfg := w.collectConnConfig()
		renderOK, err := testConnection(cfg)
		if err != nil {
			w.st.Log("向导: 连接测试失败: %v", err)
		}
		w.mw.Synchronize(func() {
			w.busy = false
			w.renderOK = renderOK
			switch {
			case err != nil:
				w.connStatusLbl.SetText("连接失败: " + friendlyErrText(err) + ", 请稍后重试")
				w.connStatusLbl.SetTextColor(colRed)
			case !renderOK:
				// 不暴露"出图机离线": 以排队/繁忙表述, 并告知首张将自动无感补图
				w.connStatusLbl.SetText("连接成功 ✓ 当前出图排队较多, 首张壁纸将稍后自动无感补图")
				w.connStatusLbl.SetTextColor(colHint)
			default:
				w.connStatusLbl.SetText("连接成功 ✓ 云端服务可用, 出图服务就绪, 配额正常")
				w.connStatusLbl.SetTextColor(colGreen)
			}
		})
	}()
}

func (w *wizard) collectConnConfig() *config.Config {
	c := *w.cfg
	if w.directCheck.Checked() {
		c.DirectMode = true
		c.DirectURL = strings.TrimSpace(w.directURLEdit.Text())
		c.DirectToken = strings.TrimSpace(w.directTokEdit.Text())
	} else {
		c.DirectMode = false
	}
	return &c
}

// testAndRegister 保存连接配置并匿名注册, 成功返回 true。
func (w *wizard) testAndRegister() bool {
	cfg := w.collectConnConfig()
	if cfg.DirectMode && cfg.DirectURL == "" {
		showInfo(w.mw, "调试模式需要填写 ComfyUI 地址。")
		return false
	}
	*w.cfg = *cfg
	w.busy = true
	w.btnNext.SetEnabled(false)
	err := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		if cfg.DirectMode {
			// 调试模式(直连 ComfyUI)不依赖云端, 跳过匿名注册
			return nil
		}
		if err := ensureRegistered(ctx, w.cfg); err != nil {
			return err
		}
		return nil
	}()
	w.busy = false
	if err != nil {
		w.st.Log("testAndRegister 失败: %v", err)
		showError(w.mw, "暂时无法连接服务(%s), 请稍后重试。", friendlyErrText(err))
		w.btnNext.SetEnabled(true)
		return false
	}
	if err := w.cfg.Save(w.dir); err != nil {
		showError(w.mw, "配置保存失败: %v", err)
		w.btnNext.SetEnabled(true)
		return false
	}
	w.be, w.cc = makeBackend(w.cfg)
	// 记录出图链路可用性, 供步骤 2 首张壁纸门禁(不可用则跳过现场出图)
	pctx, pcancel := context.WithTimeout(context.Background(), 3*time.Second)
	w.renderOK = w.be.Reachable(pctx)
	pcancel()
	return true
}

// ---------- 步骤 1: 偏好问卷 ----------

// buildSurveyStep 构建问卷: 风格包(一键勾选) + 自绘筹码网格(五维) + 汇总条(计数/清空本维)。
func (w *wizard) buildSurveyStep() {
	w.ensureSurveyAnswers()

	// 风格包(面向 90/00 后审美的预设组合, 一键勾选, 可多选叠加)
	packRow, _ := walk.NewComposite(w.content)
	_ = packRow.SetLayout(walk.NewHBoxLayout())
	themedLabel(packRow, "风格包(一键勾选, 可多选):", nil, colBody)
	for _, pack := range presetPacks {
		btn, _ := walk.NewPushButton(packRow)
		btn.SetText(pack.Name)
		btn.Clicked().Attach(func() { w.applyPresetPack(pack.Keys) })
	}

	chips, err := newSurveyChips(w.content, w.surveyAnswers, w.updateSurveyCount)
	if err != nil {
		w.setStatus("问卷区域创建失败: "+err.Error(), colRed)
		return
	}
	// 返回重进时恢复上次查看的维度标签
	chips.dimIdx = w.surveyDimIdx
	chips.onDim = func(i int) { w.surveyDimIdx = i }
	w.chips = chips

	sumRow, _ := walk.NewComposite(w.content)
	_ = sumRow.SetLayout(walk.NewHBoxLayout())
	themedLabel(sumRow, "已选: 喜欢 ", nil, colBody)
	w.likesLbl = themedLabel(sumRow, "0", themeFontGroup, colGreen)
	themedLabel(sumRow, " 项 · 讨厌 ", nil, colBody)
	w.dislikesLbl = themedLabel(sumRow, "0", themeFontGroup, colRed)
	themedLabel(sumRow, " 项", nil, colBody)
	w.focusLbl = themedLabel(sumRow, "", nil, colHint)
	hsp, _ := walk.NewHSpacer(sumRow)
	clearBtn, _ := walk.NewPushButton(sumRow)
	clearBtn.SetText("清空本维")
	clearBtn.Clicked().Attach(func() { w.clearDim() })
	if bl, ok := sumRow.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(hsp, 1)
	}
	w.updateSurveyCount()
	w.enableNext("下一步")
}

// ensureSurveyAnswers 首次进入问卷时从画像反推勾选状态(之后保持内存态)。
func (w *wizard) ensureSurveyAnswers() {
	if w.surveyAnswers != nil {
		return
	}
	if w.profile == nil {
		w.profile, _ = config.LoadProfile(w.dir)
	}
	w.surveyAnswers = answersFromProfile(w.profile)
}

// updateSurveyCount 刷新汇总条: 计数 + 每维前 2 个喜欢项的"重点出图"摘要。
func (w *wizard) updateSurveyCount() {
	if w.likesLbl == nil {
		return
	}
	w.likesLbl.SetText(fmt.Sprintf("%d", surveyLikesIn(w.surveyAnswers)))
	w.dislikesLbl.SetText(fmt.Sprintf("%d", surveyDislikesIn(w.surveyAnswers)))
	w.focusLbl.SetText(focusSummary(w.surveyAnswers))
}

// clearDim 清空当前维度所有勾选(回到中立)。
func (w *wizard) clearDim() {
	if w.chips == nil {
		return
	}
	dimID := w.chips.currentDim()
	prefix := dimID + "/"
	for key := range w.surveyAnswers {
		if strings.HasPrefix(key, prefix) {
			w.surveyAnswers[key] = survey.Neutral
		}
	}
	w.chips.refresh()
	w.updateSurveyCount()
}

// presetPack 与 presetPacks 风格包预设已移至 wizard_logic.go(纯数据, 可单测)。

// applyPresetPack 将风格包内词条勾为喜欢(不覆盖其他已勾选项)。
func (w *wizard) applyPresetPack(keys []string) {
	for _, key := range keys {
		if _, ok := w.surveyAnswers[key]; ok {
			w.surveyAnswers[key] = survey.Like
		}
	}
	if w.chips != nil {
		w.chips.refresh()
	}
	w.updateSurveyCount()
}

// nextFromSurvey 问卷步骤的下一步: 校准模式合并推理返回问卷; 否则校验并保存画像。
func (w *wizard) nextFromSurvey() {
	if surveyLikesIn(w.surveyAnswers) == 0 {
		showInfo(w.mw, "请至少勾选 1 项\"喜欢\", 我才能知道你想看什么。")
		return
	}
	if surveyEqual(w.surveyAnswers, w.lastSurvey) {
		w.setStep(2) // 与上次落盘一致, 不重复写入/记信号
		return
	}
	w.profile = survey.Apply(w.profile, w.surveyAnswers)
	if err := w.profile.Save(w.dir); err != nil {
		showError(w.mw, "画像保存失败: %v", err)
		return
	}
	w.cfg.ProfileVer++
	_ = w.cfg.Save(w.dir)
	_ = signals.Append(w.dir, signals.Event{
		Type:   signals.TypeReconfigure,
		Detail: fmt.Sprintf("初始化问卷完成: 喜欢 %d 项 / 讨厌 %d 项", surveyLikesIn(w.surveyAnswers), surveyDislikesIn(w.surveyAnswers)),
	})
	w.lastSurvey = cloneSurvey(w.surveyAnswers)
	w.setStep(2)
}

// ---------- 步骤 2: 首张壁纸 ----------

func (w *wizard) buildFirstWallpaperStep() {
	w.btnExtra.SetVisible(true)
	w.btnNext.SetEnabled(false)
	w.buildPreviewView()
	if w.firstPreviewBm != nil {
		// 返回重进: 恢复上次预览
		w.firstPreview.SetImage(w.firstPreviewBm)
		w.setStatus("已应用为桌面壁纸 ✓ 喜欢就点\"喜欢\"; 不满意点\"换一张\"", colGreen)
		w.stepDone = true
		w.firstOK = true
		w.btnExtra.SetText("换一张")
		w.btnExtra.SetEnabled(true)
		w.enableNext("喜欢, 就用这张")
		return
	}
	w.firstOK = false
	w.btnExtra.SetText("换一张")
	w.btnExtra.SetEnabled(false)
	// 出图链路不可用(出图机离线/繁忙): 不现场出图, 提示稍后自动无感补图并允许跳过
	if !w.renderOK {
		w.setStatus(msgRenderBusy, colHint)
		w.stepDone = true
		w.btnExtra.SetText("重试")
		w.btnExtra.SetEnabled(true)
		w.enableNext("跳过(稍后自动补图)")
		return
	}
	w.setStatus("正在生成第一张壁纸...", colHint)
	w.genFirstWallpaper(false)
}

// buildPreviewView 创建居中的预览视图(480×270 卡片)。
func (w *wizard) buildPreviewView() {
	row, _ := walk.NewComposite(w.content)
	_ = row.SetLayout(walk.NewHBoxLayout())
	sp1, _ := walk.NewHSpacer(row)
	preview, _ := walk.NewImageView(row)
	_ = preview.SetMinMaxSize(walk.Size{Width: 480, Height: 270}, walk.Size{Width: 480, Height: 270})
	sp2, _ := walk.NewHSpacer(row)
	if bl, ok := row.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(sp1, 1)
		_ = bl.SetStretchFactor(sp2, 1)
	}
	w.firstPreview = preview
}

// startFirstCountdown 首张壁纸生成期间显示进度条与倒计时(上限 firstWallpaperTimeout)。
// 真实阶段无法从出图链路取得, 以"已用时间/上限"驱动进度, 让用户明确感知"正在进行且会自动结束"。
func (w *wizard) startFirstCountdown(seq int) {
	w.stopFirstCountdown()
	w.progress.SetValue(0)
	w.progress.SetVisible(true)
	w.setStatus("正在生成第一张壁纸… 通常 30–60 秒", colHint)
	start := time.Now()
	w.firstTick = time.NewTicker(500 * time.Millisecond)
	go func(t *time.Ticker) {
		for range t.C {
			el := time.Since(start)
			if el > firstWallpaperTimeout {
				el = firstWallpaperTimeout
			}
			pct := int(el * 100 / firstWallpaperTimeout)
			if pct > 95 {
				pct = 95
			}
			remain := int((firstWallpaperTimeout - el).Seconds())
			if remain < 0 {
				remain = 0
			}
			w.mw.Synchronize(func() {
				if seq != w.genSeq || w.firstTick == nil {
					t.Stop()
					return
				}
				w.progress.SetValue(pct)
				w.setStatus(fmt.Sprintf("正在生成第一张壁纸… 通常 30–60 秒, 最多再等 %d 秒后自动完成", remain), colHint)
			})
		}
	}(w.firstTick)
}

// stopFirstCountdown 停止进度刷新并隐藏进度条。
func (w *wizard) stopFirstCountdown() {
	if w.firstTick != nil {
		w.firstTick.Stop()
		w.firstTick = nil
	}
	w.progress.SetVisible(false)
	w.progress.SetValue(0)
}

// genFirstWallpaper 生成并应用壁纸; force=true 时排除最近主风格值("换一张")。
// 优先提服务端主动预生成的 ready 图(秒出); 无则现场出图(90s 上限)。
func (w *wizard) genFirstWallpaper(force bool) {
	if w.busy {
		return
	}
	w.busy = true
	w.btnNext.SetEnabled(false)
	w.btnExtra.SetEnabled(false)
	seq := w.genSeq
	w.startFirstCountdown(seq)
	go func() {
		data, spec, src := w.firstWallpaperPayload(force)
		if data == nil {
			// 出图链路不可用或超时: 统一"繁忙"文案, 自动跳过(不暴露技术原因)
			w.mw.Synchronize(func() {
				if seq != w.genSeq {
					return // 已导航离开, 回调过期
				}
				w.busy = false
				w.stopFirstCountdown()
				w.setStatus(msgRenderBusy, colHint)
				w.stepDone = true
				w.btnExtra.SetText("重试")
				w.btnExtra.SetEnabled(true)
				w.enableNext("跳过(稍后自动补图)")
			})
			return
		}
		path, err := w.st.SaveWallpaper(data, time.Now())
		if err == nil {
			err = desktop.SetWallpaper(path)
			w.st.SetCurrent(path)
		}
		if err != nil {
			w.st.Log("向导: 壁纸应用失败: %v", err)
		}
		_ = w.st.AppendHistory(prompt.HistoryEntry{
			At: time.Now(), Combo: spec.Combo, Positive: spec.Positive,
			Negative: spec.Negative, Seed: spec.Seed, Source: src,
		})
		w.mw.Synchronize(func() {
			if seq != w.genSeq {
				return
			}
			w.busy = false
			w.stopFirstCountdown()
			if err != nil {
				w.setStatus(msgRenderBusy, colHint)
				w.stepDone = true
				w.btnExtra.SetText("重试")
				w.btnExtra.SetEnabled(true)
				w.enableNext("跳过(稍后自动补图)")
				return
			}
			if bmp, berr := scaleCard(data, 480, 270); berr == nil {
				w.firstPreviewBm = bmp
				w.firstPreview.SetImage(bmp)
			}
			w.setStatus("已应用为桌面壁纸 ✓ 喜欢就点\"喜欢\"; 不满意点\"换一张\"", colGreen)
			w.stepDone = true
			w.firstOK = true
			w.btnExtra.SetText("换一张")
			w.btnExtra.SetEnabled(true)
			w.enableNext("喜欢, 就用这张")
		})
	}()
}

// firstWallpaperPayload 取首张壁纸字节: 先提服务端 ready 预生成(秒出), 无则现场出图(90s)。
// 返回 nil 表示当前不可出图(链路不可用/超时), 调用方按"繁忙稍后补"处理。
func (w *wizard) firstWallpaperPayload(force bool) ([]byte, prompt.Spec, string) {
	// 1) 服务端主动预生成 ready 图: 有则秒提, 避免现场等待
	if w.cc != nil && !force {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		info, err := w.cc.PregenFetch(ctx, w.cfg.ProfileVer, "")
		if err == nil && info != nil && info.Ready && info.JobID != "" {
			img, ierr := w.cc.PregenImage(ctx, info.JobID)
			cancel()
			if ierr == nil && len(img) > 0 {
				return img, prompt.Spec{Positive: info.Positive, Negative: info.Negative, Combo: info.Combo, Seed: info.Seed}, "pregen"
			}
			return nil, prompt.Spec{}, ""
		}
		cancel()
	}
	// 2) 现场出图(90s 上限)
	ctx, cancel := context.WithTimeout(context.Background(), firstWallpaperTimeout)
	defer cancel()
	history, _ := w.st.LoadHistory()
	data, spec, src, err := generateWallpaper(ctx, w.cfg, w.cc, w.be, w.profile, history, force)
	if err != nil {
		w.st.Log("向导: 生成失败: %v", err)
		return nil, prompt.Spec{}, ""
	}
	return data, spec, src
}

// retryFirstWallpaper 尚无成品时重试: 重新探测出图链路, 可用则现场出图, 否则维持"繁忙稍后补图"。
func (w *wizard) retryFirstWallpaper() {
	if w.busy {
		return
	}
	w.busy = true
	w.btnExtra.SetEnabled(false)
	w.setStatus("正在重试...", colHint)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		ok := w.be.Reachable(ctx)
		cancel()
		w.mw.Synchronize(func() {
			w.busy = false
			w.renderOK = ok
			if !ok {
				w.setStatus(msgRenderBusy, colHint)
				w.stepDone = true
				w.btnExtra.SetText("重试")
				w.btnExtra.SetEnabled(true)
				w.enableNext("跳过(稍后自动补图)")
				return
			}
			w.genFirstWallpaper(false)
		})
	}()
}

// finishFirstWallpaper 喜欢/跳过当前壁纸: 记弱正向信号 → 静默安装(计划任务+快捷方式) → 完成页。
func (w *wizard) finishFirstWallpaper() {
	if !w.firstAccepted && w.firstOK {
		_ = signals.Append(w.dir, signals.Event{Type: signals.TypeStyleKeep, Detail: "初始化首张壁纸: 喜欢"})
		w.firstAccepted = true
	}
	w.registerTask()
	w.setStep(3)
}

// ---------- 步骤 3: 完成(静默安装 + 注册) ----------

// registerTask 静默安装: 复制 exe 到 bin\AIWallpaper.exe → 注册每小时计划任务 →
// 建桌面/开始菜单快捷方式 → 写卸载项。用户无需参与; 结果在完成页与状态行体现。
func (w *wizard) registerTask() {
	exe, err := os.Executable()
	if err != nil {
		w.st.Log("向导: 定位程序路径失败: %v", err)
		w.regOK = false
		return
	}
	w.cfg.PhaseMinutes = phaseFromUser(w.cfg.UserID)
	dst, err := install.Setup(w.st.BinDir(), exe, update.CurrentVersion, w.cfg.PhaseMinutes)
	if err != nil {
		w.st.Log("向导: 安装/注册失败: %v", err)
		w.regOK = false
		_ = w.cfg.Save(w.dir)
		return
	}
	w.installedPath = dst
	w.cfg.TaskTickHours = scheduler.TickHours
	// 无感自更新留痕: 安装即当前版本首次就位(后续版本变化由 tick 更新此时间)
	w.cfg.LastVersion = update.CurrentVersion
	if w.cfg.LastUpdate == "" {
		w.cfg.LastUpdate = time.Now().Format(time.RFC3339)
	}
	_ = w.cfg.Save(w.dir)
	w.regOK = true
	w.st.Log("向导: 已安装并注册计划任务 → %s", dst)
}

// ---------- 步骤 3: 完成页 ----------

func (w *wizard) buildDoneStep() {
	checkLbl := themedLabel(w.content, "✔", themeFontDone, colGreen)
	_ = checkLbl.SetTextAlignment(walk.AlignCenter)
	title := themedLabel(w.content, "初始化完成", themeFontStep, colTitle)
	_ = title.SetTextAlignment(walk.AlignCenter)

	if w.regOK {
		w.setStatus(fmt.Sprintf("已开启每小时自动换图 ✓ 桌面与开始菜单已添加\"%s\"快捷方式", config.AppNameCN), colGreen)
	} else {
		w.setStatus("自动换图将由系统后台稍后完成注册; 不影响你正常使用", colHint)
	}

	body := themedLabel(w.content,
		"之后每小时由 Windows 计划任务静默出图并自动更换壁纸, 本程序不会常驻后台\n"+
			"关闭窗口后系统中没有任何本程序的进程; 需要时从桌面快捷方式或重新运行即可打开设置面板\n"+
			"在设置面板里你可以随时: 调整偏好问卷 · 修改换图频率 · 暂停换图 · 卸载本程序\n"+
			"偏好也会在使用中自动微调(重新勾选问卷/重新应用某张壁纸/满意度回访都会影响后续出图)",
		nil, colBody)
	_ = body.SetTextAlignment(walk.AlignCenter)

	tip := themedLabel(w.content, "提示: 安装已完成, 你下载目录里的原始安装包文件现在可以删除了。", nil, colHint)
	_ = tip.SetTextAlignment(walk.AlignCenter)

	recap := themedLabel(w.content, w.surveyRecap(), nil, colHint)
	_ = recap.SetTextAlignment(walk.AlignCenter)
	w.enableNext("完成")
}

// surveyRecap 完成页回显所选偏好(按维度分组)。
func (w *wizard) surveyRecap() string {
	w.ensureSurveyAnswers()
	return surveyRecapText(w.surveyAnswers)
}
