package ui

import (
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lxn/walk"

	"wallpaper/internal/backend"
	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/desktop"
	"wallpaper/internal/probe"
	"wallpaper/internal/prompt"
	"wallpaper/internal/scheduler"
	"wallpaper/internal/signals"
	"wallpaper/internal/store"
	"wallpaper/internal/survey"
	"wallpaper/internal/taxonomy"
)

// wizard 初始化向导(5 步; 左侧自绘步骤导航, 允许返回已完成步骤)。
// 流程: 连接服务 → 偏好问卷(五维三态筹码, 可选探针校准) → 首张壁纸(喜欢/换一张) → 注册计划任务 → 完成。
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

	// 步骤 1 可选: 图片探针校准(子流程)
	calibMode   bool
	calibCombos []probe.Combo
	probeImages map[string][]byte
	grid        *probeGrid

	// 步骤 2: 首张壁纸
	firstPreview   *walk.ImageView
	firstPreviewBm *walk.Bitmap // 返回重进时恢复预览
	firstOK        bool         // 当前壁纸已成功生成并应用
	firstAccepted  bool         // 是否已点过"喜欢"(避免重复信号)
	genSeq         int          // 生成序号(导航离开后使在途回调失效)

	// 步骤 3/4
	finalized bool // 是否已写过完成信号

	// 步骤 0: 连接设置控件
	urlEdit       *walk.LineEdit
	directCheck   *walk.CheckBox
	directURLEdit *walk.LineEdit
	directTokEdit *walk.LineEdit
	connStatusLbl *walk.Label
}

var navTitles = []string{"连接服务", "偏好问卷", "首张壁纸", "自动更新", "完成"}

// RunWizard 打开初始化向导(阻塞直到窗口关闭)。
func RunWizard(dir string, st *store.Store, cfg *config.Config) {
	w := &wizard{
		cfg:         cfg,
		dir:         dir,
		st:          st,
		probeImages: map[string][]byte{},
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
	mw.SetSize(fitWorkArea(mw, walk.Size{Width: 1040, Height: 720}))
	mw.SetMinMaxSize(fitWorkArea(mw, walk.Size{Width: 900, Height: 620}), walk.Size{})
	_ = mw.SetLayout(walk.NewVBoxLayout())
	mw.SetBackground(brushWhite)

	// 顶栏: 青绿渐变 + 产品名
	header, err := walk.NewCustomWidgetPixels(mw, 0, func(canvas *walk.Canvas, update walk.Rectangle) error {
		s := scaleFunc(mw.DPI())
		cw := clientWidthPx(mw.Handle())
		if cw <= 0 {
			cw = update.Width
		}
		_ = canvas.GradientFillRectanglePixels(colAccentDk, colAccent, walk.Horizontal, update)
		_ = canvas.DrawTextPixels(config.AppNameCN, themeFontHeader, colWhite,
			walk.Rectangle{X: s(22), Y: 0, Width: cw - s(44), Height: update.Height}, walk.TextVCenter|walk.TextSingleLine|walk.TextLeft)
		_ = canvas.DrawTextPixels("初始化向导", themeFontReg, colHeaderSub,
			walk.Rectangle{X: s(22), Y: 0, Width: cw - s(44), Height: update.Height}, walk.TextVCenter|walk.TextSingleLine|walk.TextRight)
		return nil
	})
	if err != nil {
		return err
	}
	_ = header.SetMinMaxSize(walk.Size{Height: 52}, walk.Size{Height: 52})

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
	return nil
}

// navClick 左侧导航跳转: 仅允许返回已解锁步骤(探针校准中锁定)。
func (w *wizard) navClick(n int) {
	if w.calibMode {
		return
	}
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
		w.titleLbl.SetText("第 1 步 / 共 5 步 · 连接出图服务")
		w.hintLbl.SetText("客户端所有出图与大模型调用都经云端代理, 用于统计与控制调用量; 也可直连局域网 ComfyUI 调试。")
		w.buildConnStep()
	case 1:
		w.titleLbl.SetText("第 2 步 / 共 5 步 · 勾选你的偏好")
		w.hintLbl.SetText("点圆角筹码循环切换: 中立 → ✓ 喜欢 → ✕ 讨厌; 顶部标签可切换维度。")
		w.buildSurveyStep()
	case 2:
		w.titleLbl.SetText("第 3 步 / 共 5 步 · 生成第一张壁纸")
		w.hintLbl.SetText("按你的屏幕分辨率出图并立即应用; 不满意点\"换一张\"。")
		w.buildFirstWallpaperStep()
	case 3:
		w.titleLbl.SetText("第 4 步 / 共 5 步 · 注册每小时自动更新")
		w.hintLbl.SetText("计划任务每小时静默出图换壁纸; 本程序不会常驻后台。")
		w.buildRegisterStep()
	case 4:
		w.titleLbl.SetText("第 5 步 / 共 5 步 · 完成")
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
		if w.stepDone {
			if !w.finalized {
				w.cfg.Initialized = true
				_ = w.cfg.Save(w.dir)
				_ = signals.Append(w.dir, signals.Event{Type: signals.TypeReinitialized, Detail: "初始化向导完成"})
				w.finalized = true
			}
			w.setStep(4)
		} else {
			w.registerTask() // 重试
		}
	case 4:
		w.mw.Close()
	}
}

// onExtra 附加按钮分发(问卷校准入口 / 首张壁纸换一张)。
func (w *wizard) onExtra() {
	switch w.step {
	case 1:
		w.startCalibration()
	case 2:
		if w.stepDone && !w.busy {
			_ = signals.Append(w.dir, signals.Event{Type: signals.TypeStyleReject, Detail: "初始化首张壁纸: 换一张"})
			w.genFirstWallpaper(w.firstOK) // 已有成品才需要"换个风格"
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
	w.urlEdit, _ = addLineRow(cloudGroup, "服务地址:", w.cfg.CloudURL, 420)
	themedLabel(cloudGroup, "客户端所有出图与大模型调用都经云端代理(自部署时填写你的服务地址); 本地调试可在下方勾选直连模式。", nil, colHint)

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
	w.enableNext("保存并继续")
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
		err := testConnection(cfg)
		w.mw.Synchronize(func() {
			w.busy = false
			if err != nil {
				w.connStatusLbl.SetText("连接失败: " + err.Error())
				w.connStatusLbl.SetTextColor(colRed)
			} else {
				w.connStatusLbl.SetText("连接成功 ✓ 云端服务可用, 配额正常")
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
		c.CloudURL = strings.TrimSpace(w.urlEdit.Text())
	}
	return &c
}

// testAndRegister 保存连接配置并匿名注册, 成功返回 true。
func (w *wizard) testAndRegister() bool {
	cfg := w.collectConnConfig()
	if !cfg.DirectMode && cfg.CloudURL == "" {
		showInfo(w.mw, "请填写云端服务地址。")
		return false
	}
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
		showError(w.mw, "%v", err)
		w.btnNext.SetEnabled(true)
		return false
	}
	if err := w.cfg.Save(w.dir); err != nil {
		showError(w.mw, "配置保存失败: %v", err)
		w.btnNext.SetEnabled(true)
		return false
	}
	w.be, w.cc = makeBackend(w.cfg)
	return true
}

// ---------- 步骤 1: 偏好问卷 ----------

// buildSurveyStep 构建问卷: 风格包(一键勾选) + 自绘筹码网格(五维) + 汇总条(计数/清空本维)。
func (w *wizard) buildSurveyStep() {
	w.btnExtra.SetText("图片探针校准 (可选)")
	w.btnExtra.SetVisible(true)
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
	w.surveyAnswers = map[string]survey.Attitude{}
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			w.surveyAnswers[config.Key(d.ID, v.ID)] = attitudeOfWeight(w.profile.Get(d.ID, v.ID))
		}
	}
}

// attitudeOfWeight 按画像权重反推问卷三态(与落盘阈值一致: ≥0.6 喜欢 / ≤0.25 讨厌)。
func attitudeOfWeight(wgt float64) survey.Attitude {
	switch {
	case wgt >= 0.6:
		return survey.Like
	case wgt <= 0.25:
		return survey.Dislike
	default:
		return survey.Neutral
	}
}

// updateSurveyCount 刷新汇总条: 计数 + 每维前 2 个喜欢项的"重点出图"摘要。
func (w *wizard) updateSurveyCount() {
	if w.likesLbl == nil {
		return
	}
	w.likesLbl.SetText(fmt.Sprintf("%d", w.surveyLikes()))
	w.dislikesLbl.SetText(fmt.Sprintf("%d", w.surveyDislikes()))
	var parts []string
	for _, d := range taxonomy.Dimensions {
		n := 0
		for _, v := range d.Values {
			if w.surveyAnswers[config.Key(d.ID, v.ID)] == survey.Like {
				parts = append(parts, v.NameCN)
				n++
				if n >= 2 {
					break
				}
			}
		}
	}
	if len(parts) > 6 {
		parts = parts[:6]
	}
	if len(parts) == 0 {
		w.focusLbl.SetText("")
	} else {
		w.focusLbl.SetText("  将重点出图: " + strings.Join(parts, " · "))
	}
}

func (w *wizard) surveyLikes() int {
	n := 0
	for _, a := range w.surveyAnswers {
		if a == survey.Like {
			n++
		}
	}
	return n
}

func (w *wizard) surveyDislikes() int {
	n := 0
	for _, a := range w.surveyAnswers {
		if a == survey.Dislike {
			n++
		}
	}
	return n
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

// presetPack 一个风格包(面向 90/00 后审美的预设组合)。
type presetPack struct {
	Name string   // 界面按钮文案
	Keys []string // config.Key(dim, val) 列表
}

// presetPacks 风格包(每包覆盖 5 维, 点选即勾为喜欢, 可多选叠加并再微调)。
var presetPacks = []presetPack{
	{Name: "潮酷未来", Keys: []string{
		config.Key(taxonomy.DimStyle, "cyberpunk"),
		config.Key(taxonomy.DimStyle, "neonart"),
		config.Key(taxonomy.DimSubject, "citynight"),
		config.Key(taxonomy.DimSubject, "space"),
		config.Key(taxonomy.DimPalette, "neon"),
		config.Key(taxonomy.DimMood, "vibrant"),
		config.Key(taxonomy.DimComposition, "silhouette"),
	}},
	{Name: "二次元", Keys: []string{
		config.Key(taxonomy.DimStyle, "anime"),
		config.Key(taxonomy.DimStyle, "anime3d"),
		config.Key(taxonomy.DimSubject, "mecha"),
		config.Key(taxonomy.DimSubject, "pet"),
		config.Key(taxonomy.DimPalette, "pastel"),
		config.Key(taxonomy.DimMood, "vibrant"),
		config.Key(taxonomy.DimComposition, "centered"),
	}},
	{Name: "国风雅韵", Keys: []string{
		config.Key(taxonomy.DimStyle, "ink"),
		config.Key(taxonomy.DimStyle, "neochinese"),
		config.Key(taxonomy.DimStyle, "papercut"),
		config.Key(taxonomy.DimSubject, "oldstreet"),
		config.Key(taxonomy.DimSubject, "cloudsea"),
		config.Key(taxonomy.DimPalette, "morandi"),
		config.Key(taxonomy.DimMood, "serene"),
		config.Key(taxonomy.DimComposition, "wide"),
	}},
	{Name: "治愈日常", Keys: []string{
		config.Key(taxonomy.DimStyle, "film"),
		config.Key(taxonomy.DimStyle, "watercolor"),
		config.Key(taxonomy.DimSubject, "pet"),
		config.Key(taxonomy.DimSubject, "plants"),
		config.Key(taxonomy.DimSubject, "food"),
		config.Key(taxonomy.DimPalette, "warm"),
		config.Key(taxonomy.DimMood, "cozy"),
		config.Key(taxonomy.DimComposition, "macro"),
	}},
	{Name: "极简高级", Keys: []string{
		config.Key(taxonomy.DimStyle, "minimal"),
		config.Key(taxonomy.DimSubject, "abstract"),
		config.Key(taxonomy.DimSubject, "building"),
		config.Key(taxonomy.DimPalette, "dark"),
		config.Key(taxonomy.DimMood, "mysterious"),
		config.Key(taxonomy.DimComposition, "centered"),
	}},
}

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
	if w.calibMode {
		w.mergeCalibration()
		w.calibMode = false
		w.setStep(1)
		return
	}
	if w.surveyLikes() == 0 {
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
		Detail: fmt.Sprintf("初始化问卷完成: 喜欢 %d 项 / 讨厌 %d 项", w.surveyLikes(), w.surveyDislikes()),
	})
	w.lastSurvey = cloneSurvey(w.surveyAnswers)
	w.setStep(2)
}

func surveyEqual(a, b map[string]survey.Attitude) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func cloneSurvey(m map[string]survey.Attitude) map[string]survey.Attitude {
	out := make(map[string]survey.Attitude, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// ---------- 步骤 1 可选: 图片探针校准 ----------

// startCalibration 问卷页点"图片探针校准": 内容区切换为探针生成流程。
func (w *wizard) startCalibration() {
	w.calibMode = true
	w.btnExtra.SetVisible(false)
	w.btnNext.SetEnabled(false)
	clearChildren(w.content)
	w.progress.SetVisible(true)
	w.progress.SetValue(0)
	w.calibCombos = probe.SurveyCombos(probe.FirstCount)
	w.probeImages = map[string][]byte{}
	w.grid = nil
	w.setStatus("正在生成 12 张探针图(约 1-2 分钟)...", colHint)
	go w.runCalibGen()
}

func (w *wizard) runCalibGen() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	imgs := acquireProbeImages(ctx, w.cfg, w.cc, w.be, w.calibCombos, func(done, total int) {
		w.mw.Synchronize(func() {
			w.progress.SetValue(done * 100 / maxInt(total, 1))
			w.setStatus(fmt.Sprintf("已完成 %d / %d", done, total), colHint)
		})
	})
	// 落盘留档(便于排查与复用)
	for _, cb := range w.calibCombos {
		if data, ok := imgs[cb.ID]; ok {
			_ = os.WriteFile(filepath.Join(w.st.ProbesDir(), cb.ID+".png"), data, 0o644)
		}
	}
	w.mw.Synchronize(func() {
		w.probeImages = imgs
		w.progress.SetVisible(false)
		if len(imgs) == 0 {
			w.setStatus("探针生成失败, 已返回问卷", colRed)
			w.calibMode = false
			w.setStep(1)
			return
		}
		grid, err := newProbeGrid(w.content, w.calibCombos, imgs, func() {
			w.btnNext.SetEnabled(w.grid.Likes() >= 3)
		})
		if err != nil {
			w.setStatus("探针网格创建失败: "+err.Error(), colRed)
			w.calibMode = false
			w.setStep(1)
			return
		}
		w.grid = grid
		if len(imgs) < len(w.calibCombos) {
			w.setStatus(fmt.Sprintf("生成完成 %d / %d(失败的可跳过)", len(imgs), len(w.calibCombos)), colHint)
		} else {
			w.setStatus("单击图片循环切换: 中立 → 喜欢 → 不喜欢; 至少 3 张喜欢后点\"完成校准\"", colHint)
		}
		w.btnNext.SetText("完成校准")
		w.btnNext.SetEnabled(false)
	})
}

// mergeCalibration 用探针点选推理画像, 与问卷画像融合(0.35/0.65), 结果反推回勾选状态。
func (w *wizard) mergeCalibration() {
	if w.grid == nil || w.grid.Likes() < 3 {
		return
	}
	w.profile = probe.Infer(w.calibCombos, w.grid.Choices(), w.profile)
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			w.surveyAnswers[config.Key(d.ID, v.ID)] = attitudeOfWeight(w.profile.Get(d.ID, v.ID))
		}
	}
	w.lastSurvey = nil // 校准后强制下次落盘
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

// genFirstWallpaper 生成并应用壁纸; force=true 时排除最近主风格值("换一张")。
func (w *wizard) genFirstWallpaper(force bool) {
	if w.busy {
		return
	}
	w.busy = true
	w.btnNext.SetEnabled(false)
	w.btnExtra.SetEnabled(false)
	w.setStatus("正在生成壁纸(约 30 秒)...", colHint)
	seq := w.genSeq
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		history, _ := w.st.LoadHistory()
		data, spec, err := generateWallpaper(ctx, w.cfg, w.cc, w.be, w.profile, history, force)
		if err != nil {
			w.mw.Synchronize(func() {
				if seq != w.genSeq {
					return // 已导航离开, 回调过期
				}
				w.busy = false
				w.setStatus("生成失败: "+err.Error(), colRed)
				w.stepDone = true
				w.btnExtra.SetText("重试")
				w.btnExtra.SetEnabled(true)
				w.enableNext("跳过(稍后自动生成)")
			})
			return
		}
		path, err := w.st.SaveWallpaper(data, time.Now())
		if err == nil {
			err = desktop.SetWallpaper(path)
			w.st.SetCurrent(path)
		}
		_ = w.st.AppendHistory(prompt.HistoryEntry{
			At: time.Now(), Combo: spec.Combo, Positive: spec.Positive,
			Negative: spec.Negative, Seed: spec.Seed, Source: "local",
		})
		w.mw.Synchronize(func() {
			if seq != w.genSeq {
				return
			}
			w.busy = false
			if err != nil {
				w.setStatus("壁纸应用失败: "+err.Error(), colRed)
				w.stepDone = true
				w.btnExtra.SetText("重试")
				w.btnExtra.SetEnabled(true)
				w.enableNext("跳过(稍后自动生成)")
				return
			}
			if bmp, err := scaleCard(data, 480, 270); err == nil {
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

// finishFirstWallpaper 喜欢当前壁纸: 记弱正向信号并进入注册步骤。
func (w *wizard) finishFirstWallpaper() {
	if !w.firstAccepted {
		_ = signals.Append(w.dir, signals.Event{Type: signals.TypeStyleKeep, Detail: "初始化首张壁纸: 喜欢"})
		w.firstAccepted = true
	}
	w.setStep(3)
}

// ---------- 步骤 3: 注册计划任务 ----------

func (w *wizard) buildRegisterStep() {
	group, _ := walk.NewGroupBox(w.content)
	group.SetTitle("每小时自动更新")
	group.SetBackground(brushWhite)
	_ = group.SetLayout(walk.NewVBoxLayout())
	themedLabel(group, "计划任务每小时静默出图并自动更换壁纸; 本程序不会常驻后台。", nil, colBody)
	themedLabel(group, "注册成功后即可关闭窗口; 手动运行本程序随时可以修改设置。", nil, colHint)
	w.setStatus("", colHint)
	w.enableNext("注册")
	w.registerTask()
}

func (w *wizard) registerTask() {
	exe, err := os.Executable()
	if err != nil {
		w.setStatus("无法定位程序路径: "+err.Error(), colRed)
		return
	}
	dst := filepath.Join(w.st.BinDir(), "wallpaper.exe")
	if !samePath(exe, dst) {
		if err := copyFile(exe, dst); err != nil {
			w.setStatus("复制程序失败: "+err.Error(), colRed)
			return
		}
	}
	w.cfg.PhaseMinutes = phaseFromUser(w.cfg.UserID)
	if err := scheduler.Register(dst, w.cfg.IntervalHours, w.cfg.PhaseMinutes); err != nil {
		w.setStatus("注册失败: "+err.Error(), colRed)
		w.enableNext("重试注册")
		return
	}
	_ = w.cfg.Save(w.dir)
	state, _ := scheduler.QueryState()
	w.stepDone = true
	w.setStatus(fmt.Sprintf("计划任务已注册 ✓ 每小时第 %d 分钟前后自动换壁纸 (%s)", w.cfg.PhaseMinutes, state), colGreen)
	w.enableNext("下一步")
}

// ---------- 步骤 4: 完成 ----------

func (w *wizard) buildDoneStep() {
	checkLbl := themedLabel(w.content, "✔", themeFontDone, colGreen)
	_ = checkLbl.SetTextAlignment(walk.AlignCenter)
	title := themedLabel(w.content, "初始化完成", themeFontStep, colTitle)
	_ = title.SetTextAlignment(walk.AlignCenter)
	body := themedLabel(w.content,
		"之后每小时由 Windows 计划任务静默出图并自动更换壁纸\n"+
			"本程序不会常驻后台: 此刻关闭窗口后, 系统中没有任何本程序的进程\n"+
			"需要修改设置时, 重新运行本程序即可(手动运行 = 设置面板)\n"+
			"偏好会在使用中自动微调: 重新勾选问卷/重新应用某张壁纸/满意度回访\n"+
			"都会被记为漂移依据, 影响后续出图方向",
		nil, colBody)
	_ = body.SetTextAlignment(walk.AlignCenter)
	recap := themedLabel(w.content, w.surveyRecap(), nil, colHint)
	_ = recap.SetTextAlignment(walk.AlignCenter)
	w.enableNext("完成")
}

// surveyRecap 完成页回显所选偏好(按维度分组)。
func (w *wizard) surveyRecap() string {
	w.ensureSurveyAnswers()
	var groups []string
	for _, d := range taxonomy.Dimensions {
		var names []string
		for _, v := range d.Values {
			if w.surveyAnswers[config.Key(d.ID, v.ID)] == survey.Like {
				names = append(names, v.NameCN)
			}
		}
		if len(names) > 0 {
			groups = append(groups, d.NameCN+": "+strings.Join(names, " · "))
		}
	}
	if len(groups) == 0 {
		return ""
	}
	return "你的偏好\n" + strings.Join(groups, "\n")
}

// ---------- 通用小工具 ----------

func phaseFromUser(userID string) int {
	h := fnv.New64a()
	_, _ = h.Write([]byte(userID))
	return int(h.Sum64() % 60)
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o755)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
