package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/lxn/walk"
	qrcode "github.com/skip2/go-qrcode"

	"wallpaper/internal/backend"
	"wallpaper/internal/cloud"
	"wallpaper/internal/config"
	"wallpaper/internal/desktop"
	"wallpaper/internal/install"
	"wallpaper/internal/policy"
	"wallpaper/internal/scheduler"
	"wallpaper/internal/signals"
	"wallpaper/internal/store"
	"wallpaper/internal/survey"
	"wallpaper/internal/taxonomy"
	"wallpaper/internal/tick"
	"wallpaper/internal/update"
)

// settings 设置面板(已初始化后的手动运行界面)。
type settings struct {
	mw  *walk.MainWindow
	dir string
	st  *store.Store
	cfg *config.Config
	cc  *cloud.Client
	be  backend.ImageBackend

	restartWizard bool

	// 当前壁纸页
	curIV   *walk.ImageView
	curLbl  *walk.Label
	curPath string

	// 偏好页
	chips       *surveyChips
	answers     map[string]survey.Attitude
	baseAnswers map[string]survey.Attitude // 关闭窗口时判断"未保存修改"的基线
	kwLB        *walk.ListBox
	kwEdit      *walk.LineEdit

	// 设置页
	directCheck *walk.CheckBox
	directURL   *walk.LineEdit
	directTok   *walk.LineEdit
	directRow   *walk.Composite
	connLbl     *walk.Label
	intervalCB  *walk.ComboBox
	autoCk      *walk.CheckBox
	pauseCB     *walk.ComboBox
	pauseLB     *walk.Label
	taskLbl     *walk.Label
	askCk       *walk.CheckBox

	// 历史页
	histModel *wallpaperListModel
	histLB    *walk.ListBox
	histIV    *walk.ImageView

	// 同步页
	userLbl *walk.Label
	syncLbl *walk.Label

	// 更新与回访页
	versionLbl *walk.Label
	updateLbl  *walk.Label
	askLbl     *walk.Label
	askSpin    *walk.NumberEdit
}

// RunSettings 打开设置面板(阻塞直到窗口关闭); 若请求了重新初始化则随后打开向导。
func RunSettings(dir string, st *store.Store, cfg *config.Config) {
	st.Log("settings: 启动, direct_mode=%v", cfg.DirectMode)
	s := &settings{dir: dir, st: st, cfg: cfg}
	if err := s.build(); err != nil {
		st.Log("settings 窗口创建失败: %v", err)
		showError(nil, "窗口创建失败: %v", err)
		return
	}
	st.Log("settings: build 完成")
	s.be, s.cc = makeBackend(s.cfg)
	st.Log("settings: backend 就绪, 进入消息循环")
	s.mw.Show()
	s.mw.Run()
	st.Log("settings: 消息循环退出")
	if s.restartWizard {
		s.cfg.Initialized = false
		_ = s.cfg.Save(s.dir)
		RunWizard(s.dir, s.st, s.cfg)
	}
}

func (s *settings) build() error {
	initTheme()
	mw, err := walk.NewMainWindow()
	if err != nil {
		return err
	}
	s.mw = mw
	mw.SetTitle(config.AppNameCN + " — 设置")
	// 标题栏/任务栏图标: 从 exe 资源加载(rsrc 嵌入的 RT_GROUP_ICON id=2; manifest 占用 id=1)
	if icon, err := walk.NewIconFromResourceId(2); err == nil {
		_ = mw.SetIcon(icon)
	}
	mw.SetBackground(brushPageBg)
	fixedSize := fitWorkArea(mw, walk.Size{Width: 980, Height: 680})
	mw.SetSize(fixedSize)
	mw.SetMinMaxSize(fixedSize, fixedSize) // 固定尺寸: 不允许拉伸宽高
	_ = mw.SetLayout(walk.NewVBoxLayout())

	// 顶栏: 无边框标题栏(拖动热区 + 右上角扁平 ✕)
	if _, err := newTitleBar(mw, mw, config.AppNameCN, "设置", 48, func() { s.confirmClose() }); err != nil {
		return err
	}

	tab, err := walk.NewTabWidget(mw)
	if err != nil {
		return err
	}
	if err := s.buildCurrentPage(tab); err != nil {
		return err
	}
	if err := s.buildProfilePage(tab); err != nil {
		return err
	}
	if err := s.buildHistoryPage(tab); err != nil {
		return err
	}
	if err := s.buildSettingsPage(tab); err != nil {
		return err
	}
	if bl, ok := mw.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(tab, 1)
	}
	enableCustomFrame(mw) // 无边框(保留阴影/贴靠/边缘拉伸)
	return nil
}

func newPage(tab *walk.TabWidget, title string) (*walk.TabPage, error) {
	page, err := walk.NewTabPage()
	if err != nil {
		return nil, err
	}
	page.SetTitle(title)
	page.SetBackground(brushPageBg)
	if err := page.SetLayout(walk.NewVBoxLayout()); err != nil {
		return nil, err
	}
	return page, tab.Pages().Add(page)
}

// ---------- 偏好页 ----------

func (s *settings) buildProfilePage(tab *walk.TabWidget) error {
	page, err := newPage(tab, "偏好")
	if err != nil {
		return err
	}
	profile, _ := config.LoadProfile(s.dir)
	hint, _ := walk.NewLabel(page)
	hint.SetText("点一下 = 喜欢 ✓, 再点 = 不喜欢 ✕, 再点 = 取消; 改完点\"保存偏好\"生效。")

	// 风格包(与初始化向导同一套预设, 一键勾选可多选叠加; 改动需点"保存偏好"落盘)
	packRow, _ := walk.NewComposite(page)
	_ = packRow.SetLayout(walk.NewHBoxLayout())
	themedLabel(packRow, "风格包(一键勾选, 可多选):", nil, colBody)
	for _, pack := range presetPacks {
		btn, _ := walk.NewPushButton(packRow)
		btn.SetText(pack.Name)
		btn.Clicked().Attach(func() { s.applyPresetPack(pack.Keys) })
	}

	// 三态点选(与初始化向导同一交互: 维度标签 + 筹码网格)
	s.answers = map[string]survey.Attitude{}
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			s.answers[config.Key(d.ID, v.ID)] = attitudeOfWeight(profile.Get(d.ID, v.ID))
		}
	}
	s.baseAnswers = cloneSurvey(s.answers)
	if s.chips, err = newSurveyChips(page, s.answers, nil); err != nil {
		return err
	}

	// 自定义关键词(词典之外的个人喜好, 改动即时生效)
	kwTitle, _ := walk.NewLabel(page)
	kwTitle.SetText("自定义关键词(词典之外的个人喜好, 如: 猫咪、白描、高达; 改动即时生效):")
	kwRow, _ := walk.NewComposite(page)
	_ = kwRow.SetLayout(walk.NewHBoxLayout())
	s.kwEdit, _ = walk.NewLineEdit(kwRow)
	_ = s.kwEdit.SetMinMaxSize(walk.Size{Width: 240}, walk.Size{Width: 240})
	addKwBtn, _ := walk.NewPushButton(kwRow)
	addKwBtn.SetText("添加")
	addKwBtn.Clicked().Attach(func() { s.addKeyword() })
	delKwBtn, _ := walk.NewPushButton(kwRow)
	delKwBtn.SetText("删除选中")
	delKwBtn.Clicked().Attach(func() { s.delKeyword() })
	s.kwLB, _ = walk.NewListBox(page)
	_ = s.kwLB.SetMinMaxSize(walk.Size{Width: 420, Height: 60}, walk.Size{Width: 420, Height: 60})
	s.reloadKeywords(profile)

	ctxCk, _ := walk.NewCheckBox(page)
	ctxCk.SetText("跟随节日/节气氛围(默认开启; 仅画面氛围轻推, 不改你的画风, 可随时关闭)")
	ctxCk.SetChecked(!s.cfg.DisableContext)
	ctxCk.CheckedChanged().Attach(func() {
		s.cfg.DisableContext = !ctxCk.Checked()
		_ = s.cfg.Save(s.dir)
	})

	btnRow, _ := walk.NewComposite(page)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	saveBtn, _ := walk.NewPushButton(btnRow)
	saveBtn.SetText("保存偏好")
	saveBtn.Clicked().Attach(func() { s.saveProfileEdits(false) })
	reBtn, _ := walk.NewPushButton(btnRow)
	reBtn.SetText("重新初始化向导")
	reBtn.Clicked().Attach(func() {
		if walk.MsgBox(s.mw, config.AppNameCN,
			"将重新运行初始化向导并重新勾选偏好问卷。\n这会被记为一次偏好漂移依据, 帮助后续出图更贴近你。\n继续吗?",
			walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) == walk.DlgCmdYes {
			s.restartWizard = true
			s.mw.Close()
		}
	})
	saveAllBtn, _ := walk.NewPushButton(btnRow)
	saveAllBtn.SetText("打开数据目录")
	saveAllBtn.Clicked().Attach(func() { openFolder(s.dir) })
	return nil
}

// ---------- 当前壁纸页 ----------

// buildCurrentPage 反馈台: 当前壁纸大图 + 喜欢/不喜欢/立即换一张。
func (s *settings) buildCurrentPage(tab *walk.TabWidget) error {
	page, err := newPage(tab, "当前壁纸")
	if err != nil {
		return err
	}
	s.curPath = s.st.CurrentPath()

	title, _ := walk.NewLabel(page)
	title.SetText("这是你桌面上的当前壁纸, 给它一点反馈吧(改动会随下一轮出图生效):")

	if s.curPath != "" {
		if data, err := os.ReadFile(s.curPath); err == nil {
			if bmp, err := scalePlain(data, 560, 315); err == nil {
				s.curIV, _ = walk.NewImageView(page)
				if s.curIV != nil {
					s.curIV.SetImage(bmp)
					_ = s.curIV.SetMinMaxSize(walk.Size{Width: 560, Height: 315}, walk.Size{Width: 560, Height: 315})
				}
			}
		}
	}
	if s.curIV == nil {
		empty, _ := walk.NewLabel(page)
		empty.SetText("(暂无壁纸留档, 等待下一轮自动出图)")
	}

	btnRow, _ := walk.NewComposite(page)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	likeBtn, _ := walk.NewPushButton(btnRow)
	likeBtn.SetText("喜欢这张")
	likeBtn.Clicked().Attach(func() { s.rateCurrent(signals.TypeLiked, "喜欢") })
	dislikeBtn, _ := walk.NewPushButton(btnRow)
	dislikeBtn.SetText("不喜欢")
	dislikeBtn.Clicked().Attach(func() { s.rateCurrent(signals.TypeDisliked, "不喜欢") })
	swapBtn, _ := walk.NewPushButton(btnRow)
	swapBtn.SetText("立即换一张")
	swapBtn.Clicked().Attach(func() { s.swapNow(swapBtn) })

	s.curLbl, _ = walk.NewLabel(page)
	s.curLbl.SetText("")

	hint, _ := walk.NewLabel(page)
	if p, err := config.LoadProfile(s.dir); err == nil && !p.UpdatedAt.IsZero() {
		days := int(time.Since(p.UpdatedAt).Hours() / 24)
		if days <= 0 {
			hint.SetText("上次调整偏好: 今天 — 想微调可到\"偏好\"页")
		} else {
			hint.SetText(fmt.Sprintf("上次调整偏好: %d 天前 — 想微调可到\"偏好\"页", days))
		}
	}
	return nil
}

// rateCurrent 记录当前壁纸评价信号(附五维组合, 供云端 LLM 针对性调整)。
func (s *settings) rateCurrent(typ, word string) {
	if s.curPath == "" {
		showInfo(s.mw, "当前没有可评价的壁纸。")
		return
	}
	ev := signals.Event{Type: typ, Detail: filepath.Base(s.curPath)}
	if h, err := s.st.LoadHistory(); err == nil && len(h) > 0 {
		if combo := h[len(h)-1].Combo; len(combo) > 0 {
			ev.Extra = map[string]any{"combo": combo}
		}
	}
	if err := signals.Append(s.dir, ev); err != nil {
		showError(s.mw, "%v", err)
		return
	}
	if typ == signals.TypeLiked {
		s.curLbl.SetText("已记录 " + word + " — 之后会多给你这类画面 ✓")
	} else {
		s.curLbl.SetText("已记录 " + word + " — 之后会避开这类画面 ✓")
	}
}

// swapNow 立即换一张: 直接拉起隐藏的 --tick --force(绕过全部自动节律闸门),
// 异步轮询结果文件, 完成后按真实结果刷新提示与壁纸大图。
func (s *settings) swapNow(btn *walk.PushButton) {
	if !btn.Enabled() {
		return // 出图进行中, 防重复点击
	}
	btn.SetEnabled(false)
	s.curLbl.SetText("正在出图…(约 1-2 分钟, 完成后自动生效)")
	_ = os.Remove(tick.OutcomePath(s.dir)) // 清掉上次残留, 避免读到旧结果
	exe, err := os.Executable()
	if err == nil {
		var cmd *exec.Cmd
		if cmd, err = scheduler.RunTickNow(exe); err == nil {
			go s.awaitSwap(btn, cmd)
			return
		}
	}
	btn.SetEnabled(true)
	s.curLbl.SetText("触发失败: " + err.Error())
}

// awaitSwap 等待换图进程与结果文件(500ms 轮询, 上限 15 分钟), 回 UI 线程收尾。
func (s *settings) awaitSwap(btn *walk.PushButton, cmd *exec.Cmd) {
	waitCh := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waitCh) }()

	var res *tick.Outcome
	timeout := time.After(15 * time.Minute)
	poll := time.NewTicker(500 * time.Millisecond)
	defer poll.Stop()
	for res == nil {
		select {
		case <-waitCh:
			// 结果文件在进程退出前写好; 读不到 = 进程异常退出
			if o, err := tick.ReadOutcome(s.dir); err == nil {
				res = &o
			} else {
				res = &tick.Outcome{Detail: "换图进程异常退出(未写结果)"}
			}
		case <-poll.C:
			if o, err := tick.ReadOutcome(s.dir); err == nil {
				res = &o
			}
		case <-timeout:
			res = &tick.Outcome{Detail: "仍在出图中(超过 15 分钟), 可稍后查看桌面"}
		}
	}
	s.mw.Synchronize(func() {
		btn.SetEnabled(true)
		if res.Changed {
			s.curLbl.SetText("✓ 已换壁纸 — 新图已生效")
			s.refreshCurrentImage()
		} else {
			s.curLbl.SetText("未换图: " + res.Detail)
		}
	})
}

// refreshCurrentImage 换图成功后重载当前壁纸大图与路径(喜欢/不喜欢随之指向新图)。
func (s *settings) refreshCurrentImage() {
	if s.curIV == nil {
		return
	}
	p := s.st.CurrentPath()
	if p == "" {
		return
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return
	}
	bmp, err := scalePlain(data, 560, 315)
	if err != nil {
		return
	}
	s.curIV.SetImage(bmp)
	s.curPath = p
}

// keywordListModel 自定义关键词列表模型。
type keywordListModel struct {
	walk.ListModelBase
	items []string
}

func (m *keywordListModel) ItemCount() int { return len(m.items) }

func (m *keywordListModel) Value(i int) any { return m.items[i] }

// reloadKeywords 刷新关键词列表显示。
func (s *settings) reloadKeywords(profile *config.Profile) {
	if s.kwLB == nil || profile == nil {
		return
	}
	m := &keywordListModel{items: append([]string{}, profile.CustomKeywords...)}
	_ = s.kwLB.SetModel(m)
}

// addKeyword 添加自定义关键词(即时保存, 递增画像版本作废旧预生成)。
func (s *settings) addKeyword() {
	kw := strings.TrimSpace(s.kwEdit.Text())
	if kw == "" {
		return
	}
	profile, _ := config.LoadProfile(s.dir)
	profile.CustomKeywords = config.NormalizeKeywords(append(profile.CustomKeywords, kw))
	if err := profile.Save(s.dir); err != nil {
		showError(s.mw, "关键词保存失败: %v", err)
		return
	}
	s.kwEdit.SetText("")
	s.reloadKeywords(profile)
	s.saveKeywordMeta("添加 " + kw)
}

// delKeyword 删除选中的自定义关键词。
func (s *settings) delKeyword() {
	profile, _ := config.LoadProfile(s.dir)
	idx := s.kwLB.CurrentIndex()
	if idx < 0 || idx >= len(profile.CustomKeywords) {
		showInfo(s.mw, "请先在列表中选中要删除的关键词。")
		return
	}
	removed := profile.CustomKeywords[idx]
	profile.CustomKeywords = append(profile.CustomKeywords[:idx], profile.CustomKeywords[idx+1:]...)
	if err := profile.Save(s.dir); err != nil {
		showError(s.mw, "关键词保存失败: %v", err)
		return
	}
	s.reloadKeywords(profile)
	s.saveKeywordMeta("删除 " + removed)
}

// saveKeywordMeta 关键词变化 = 画像方向调整: 递增画像版本 + 记录漂移事件。
func (s *settings) saveKeywordMeta(detail string) {
	s.cfg.ProfileVer++
	_ = s.cfg.Save(s.dir)
	_ = signals.Append(s.dir, signals.Event{Type: signals.TypeKeywordsAdjust, Detail: "自定义关键词: " + detail})
}

// applyPresetPack 风格包一键勾选: 包内词条勾为"喜欢", 不覆盖其他已勾项(可多选叠加);
// 与向导 applyPresetPack 同语义, 改动需点"保存偏好"落盘。
func (s *settings) applyPresetPack(keys []string) {
	for _, key := range keys {
		if _, ok := s.answers[key]; ok {
			s.answers[key] = survey.Like
		}
	}
	if s.chips != nil {
		s.chips.refresh()
	}
}

// saveProfileEdits 保存偏好点选; quiet 时不弹"已保存"提示(关闭窗口流程用)。
// 返回是否保存成功; 成功后基线前移(视为无未保存修改)。
func (s *settings) saveProfileEdits(quiet bool) bool {
	profile, _ := config.LoadProfile(s.dir)
	// 三态点选 → 画像(喜欢 0.9 / 中立 0.3 / 不喜欢 0.05 + 避让清单)
	survey.Apply(profile, s.answers)
	if err := profile.Save(s.dir); err != nil {
		showError(s.mw, "偏好保存失败: %v", err)
		return false
	}
	s.cfg.ProfileVer++
	_ = s.cfg.Save(s.dir)
	_ = signals.Append(s.dir, signals.Event{Type: signals.TypeWeightAdjust, Detail: "设置页调整偏好"})
	s.baseAnswers = cloneSurvey(s.answers)
	if !quiet {
		showInfo(s.mw, "偏好已保存。出图将严格按你勾选的画风与题材生成。")
	}
	return true
}

// ---------- 服务与调度页 ----------

// ---------- 设置页(服务/换图/回访/更新/同步 合并) ----------

func (s *settings) buildSettingsPage(tab *walk.TabWidget) error {
	page, err := newPage(tab, "设置")
	if err != nil {
		return err
	}

	// ----- 服务(本地模式; 默认走云端) -----
	svcGroup, _ := walk.NewGroupBox(page)
	svcGroup.SetTitle("服务")
	svcGroup.SetBackground(brushWhite)
	_ = svcGroup.SetLayout(walk.NewVBoxLayout())
	s.directCheck, _ = walk.NewCheckBox(svcGroup)
	s.directCheck.SetText("本地模式: 直连局域网 ComfyUI(高级, 默认关闭; 默认走云端)")
	s.directCheck.SetChecked(s.cfg.DirectMode)
	s.directRow, _ = walk.NewComposite(svcGroup)
	_ = s.directRow.SetLayout(walk.NewVBoxLayout())
	s.directURL, _ = addLineRow(s.directRow, "ComfyUI 地址:", s.cfg.DirectURL, 420)
	s.directTok, _ = addLineRow(s.directRow, "访问令牌(可选):", s.cfg.DirectToken, 420)
	s.directRow.SetVisible(s.cfg.DirectMode)
	s.directCheck.CheckedChanged().Attach(func() { s.directRow.SetVisible(s.directCheck.Checked()) })

	// ----- 换图(用户唯一控制项): 开=按节律换图; 关=暂停, 到期自动恢复防遗忘 -----
	chGroup, _ := walk.NewGroupBox(page)
	chGroup.SetTitle("换图")
	chGroup.SetBackground(brushWhite)
	_ = chGroup.SetLayout(walk.NewVBoxLayout())
	row1, _ := walk.NewComposite(chGroup)
	_ = row1.SetLayout(walk.NewHBoxLayout())
	s.autoCk, _ = walk.NewCheckBox(row1)
	s.autoCk.SetText("自动换图")
	s.autoCk.SetChecked(s.cfg.AutoChange)
	lbl1, _ := walk.NewLabel(row1)
	lbl1.SetText("  换图频率:")
	s.intervalCB, _ = walk.NewComboBox(row1)
	_ = s.intervalCB.SetModel([]string{"1 小时", "2 小时", "3 小时", "4 小时", "6 小时", "8 小时", "12 小时"})
	_ = s.intervalCB.SetCurrentIndex(intervalIndex(s.cfg.IntervalHours))

	row2, _ := walk.NewComposite(chGroup)
	_ = row2.SetLayout(walk.NewHBoxLayout())
	pauseLbl, _ := walk.NewLabel(row2)
	pauseLbl.SetText("暂停时长(关闭自动换图时生效):")
	s.pauseCB, _ = walk.NewComboBox(row2)
	opts := s.pauseOptions()
	labels := make([]string, len(opts))
	for i, h := range opts {
		labels[i] = policy.PauseLabel(h)
	}
	_ = s.pauseCB.SetModel(labels)
	_ = s.pauseCB.SetCurrentIndex(s.pauseIndex(s.cfg.PauseHours))
	s.pauseLB, _ = walk.NewLabel(row2)

	// 计划任务: 展示注册状态 + 修复按钮(注册常被 360 等杀软拦截, 修复后必须校验确实生效)。
	row3, _ := walk.NewComposite(chGroup)
	_ = row3.SetLayout(walk.NewHBoxLayout())
	s.taskLbl, _ = walk.NewLabel(row3)
	s.taskLbl.SetText("计划任务: 检测中...")
	repairBtn, _ := walk.NewPushButton(row3)
	repairBtn.SetText("修复计划任务")
	repairBtn.Clicked().Attach(func() { s.repairTask(repairBtn) })
	go func() {
		state, err := scheduler.QueryState()
		s.mw.Synchronize(func() {
			if err != nil || state == "" {
				s.taskLbl.SetText("计划任务: 未检测到(可能被杀软拦截)")
			} else {
				s.taskLbl.SetText("计划任务: " + state)
			}
		})
	}()

	// ----- 偏好回访(开关 + 周期) -----
	askGroup, _ := walk.NewGroupBox(page)
	askGroup.SetTitle("偏好回访")
	askGroup.SetBackground(brushWhite)
	_ = askGroup.SetLayout(walk.NewVBoxLayout())
	askRow, _ := walk.NewComposite(askGroup)
	_ = askRow.SetLayout(walk.NewHBoxLayout())
	s.askCk, _ = walk.NewCheckBox(askRow)
	s.askCk.SetText("接收偏好回访(偶尔确认是否合你心意, 帮我们更懂你)")
	s.askCk.SetChecked(s.cfg.AskEnabled)
	lbl2, _ := walk.NewLabel(askRow)
	lbl2.SetText("  回访周期(天):")
	s.askSpin, _ = walk.NewNumberEdit(askRow)
	s.askSpin.SetDecimals(0)
	s.askSpin.SetRange(1, 365)
	s.askSpin.SetValue(float64(s.cfg.Satisfaction.IntervalDays))
	s.askLbl, _ = walk.NewLabel(askGroup)
	s.refreshAskLabel()

	// ----- 软件更新 -----
	upGroup, _ := walk.NewGroupBox(page)
	upGroup.SetTitle("软件更新")
	upGroup.SetBackground(brushWhite)
	_ = upGroup.SetLayout(walk.NewVBoxLayout())
	verRow, _ := walk.NewComposite(upGroup)
	_ = verRow.SetLayout(walk.NewHBoxLayout())
	s.versionLbl, _ = walk.NewLabel(verRow)
	s.versionLbl.SetText("当前版本: " + update.CurrentVersion)
	s.updateLbl, _ = walk.NewLabel(verRow)
	s.updateLbl.SetText("最后更新: " + s.lastUpdateText())
	changelogBtn, _ := walk.NewPushButton(verRow)
	changelogBtn.SetText("查看更新内容")
	changelogBtn.Clicked().Attach(func() { openURL(releaseNotesURL) })
	manualBtn, _ := walk.NewPushButton(verRow)
	manualBtn.SetText("检查更新")
	manualBtn.Clicked().Attach(func() { s.checkUpdateManual(manualBtn) })
	upNoteLbl, _ := walk.NewLabel(upGroup)
	upNoteLbl.SetText("软件在后台自动无感更新, 也可点\"检查更新\"立即获取新版本; 更新后此处会显示最后更新时间。")

	// ----- 设备同步(换机/重装领回画像) -----
	syncGroup, _ := walk.NewGroupBox(page)
	syncGroup.SetTitle("设备同步")
	syncGroup.SetBackground(brushWhite)
	_ = syncGroup.SetLayout(walk.NewVBoxLayout())
	s.userLbl, _ = walk.NewLabel(syncGroup)
	s.userLbl.SetText("匿名用户 ID: " + s.cfg.UserID + "   (零注册零登录)")
	s.syncLbl, _ = walk.NewLabel(syncGroup)
	s.syncLbl.SetText("同步状态: 正常(每轮出图自动同步)")
	syncRow, _ := walk.NewComposite(syncGroup)
	_ = syncRow.SetLayout(walk.NewHBoxLayout())
	qrBtn, _ := walk.NewPushButton(syncRow)
	qrBtn.SetText("生成配对二维码 (迁到新设备)")
	qrBtn.Clicked().Attach(func() { s.createLink() })
	redeemBtn, _ := walk.NewPushButton(syncRow)
	redeemBtn.SetText("输入配对码 (从旧设备恢复)")
	redeemBtn.Clicked().Attach(func() { s.redeemLink() })

	// ----- 底部操作行 -----
	btnRow, _ := walk.NewComposite(page)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	saveBtn, _ := walk.NewPushButton(btnRow)
	saveBtn.SetText("保存设置")
	saveBtn.Clicked().Attach(func() { s.saveService(false) })
	testBtn, _ := walk.NewPushButton(btnRow)
	testBtn.SetText("测试连接")
	testBtn.Clicked().Attach(func() {
		s.connLbl.SetText("测试中...")
		go func() {
			renderOK, err := testConnection(s.collectServiceConfig())
			if err != nil {
				s.st.Log("settings: 测试连接失败: %v", err)
			}
			s.mw.Synchronize(func() {
				switch {
				case err != nil:
					s.connLbl.SetText("连接失败: " + friendlyErrText(err) + ", 请稍后重试")
				case !renderOK:
					s.connLbl.SetText("连接成功 ✓ 当前出图排队较多, 将自动无感补图")
				default:
					s.connLbl.SetText("连接成功 ✓")
				}
			})
		}()
	})
	s.connLbl, _ = walk.NewLabel(btnRow)
	hsp, _ := walk.NewHSpacer(btnRow)
	openBtn, _ := walk.NewPushButton(btnRow)
	openBtn.SetText("打开数据目录")
	openBtn.Clicked().Attach(func() { openFolder(s.dir) })
	uninstallBtn, _ := walk.NewPushButton(btnRow)
	uninstallBtn.SetText("卸载")
	uninstallBtn.Clicked().Attach(func() {
		if proceed, removeData := confirmUninstall(s.mw); proceed {
			doUninstall(s.dir, removeData)
		}
	})
	if bl, ok := btnRow.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(hsp, 1)
	}

	s.autoCk.CheckedChanged().Attach(func() { s.updatePauseHint() })
	s.updatePauseHint()
	return nil
}

func intervalIndex(h int) int {
	values := []int{1, 2, 3, 4, 6, 8, 12}
	for i, v := range values {
		if v == h {
			return i
		}
	}
	return 0
}

func intervalValue(idx int) int {
	values := []int{1, 2, 3, 4, 6, 8, 12}
	if idx < 0 || idx >= len(values) {
		return 1
	}
	return values[idx]
}

// pauseOptions 暂停时长选项(由运营策略下发, 默认 8 小时/1 天/2 天)。
func (s *settings) pauseOptions() []int {
	return policy.Load(s.dir).PauseOptions
}

// pauseIndex 匹配暂停时长对应的下拉索引; 未设置过时默认"1 天"。
func (s *settings) pauseIndex(h int) int {
	opts := s.pauseOptions()
	for i, v := range opts {
		if v == h {
			return i
		}
	}
	for i, v := range opts {
		if v == 24 {
			return i
		}
	}
	return 0
}

// pauseHours 当前选中的暂停时长(小时)。
func (s *settings) pauseHours() int {
	opts := s.pauseOptions()
	i := s.pauseCB.CurrentIndex()
	if i < 0 || i >= len(opts) {
		i = 0
	}
	return opts[i]
}

// updatePauseHint 刷新暂停控件状态与提示(关闭自动换图时才可选暂停时长)。
func (s *settings) updatePauseHint() {
	on := s.autoCk.Checked()
	s.pauseCB.SetEnabled(!on)
	if on {
		s.pauseLB.SetText("")
		return
	}
	if t, err := time.Parse(time.RFC3339, s.cfg.PauseUntil); err == nil && time.Now().Before(t) {
		s.pauseLB.SetText(fmt.Sprintf("已暂停, 将于 %s 自动恢复", t.Format("01-02 15:04")))
		return
	}
	s.pauseLB.SetText("保存后按所选时长暂停, 到期自动恢复")
}

// repairTask 重新注册计划任务并校验确实生效; 被杀软拦截时引导用户关闭杀软后重试。
func (s *settings) repairTask(btn *walk.PushButton) {
	exe, err := os.Executable()
	if err != nil {
		showError(s.mw, "无法获取程序路径: %v", err)
		return
	}
	// installed 守卫: 与 tick 同规则, 仅安装目录(bin\)下的程序才写系统级副作用,
	// 防止 go test / 开发态进程把计划任务劫持到临时路径(实测事故: 任务 0x80070002)。
	if filepath.Base(filepath.Dir(exe)) != "bin" {
		showInfo(s.mw, "当前为便携/开发模式, 未安装计划任务, 无需修复。")
		return
	}
	btn.SetEnabled(false)
	s.taskLbl.SetText("计划任务: 修复中...")
	go func() {
		var state string
		err := scheduler.Register(exe, scheduler.TickHours, s.cfg.PhaseMinutes)
		if err == nil {
			state, err = scheduler.VerifyRegistered(exe)
		}
		s.mw.Synchronize(func() {
			btn.SetEnabled(true)
			if err != nil {
				s.taskLbl.SetText("计划任务: 修复失败")
				s.st.Log("settings: 计划任务修复失败: %v", err)
				showError(s.mw, "计划任务修复失败, 未能确认注册成功:\n%v\n\n多半是被 360安全等杀毒软件拦截。请暂时关闭杀软的拦截防护, 再点击\"修复计划任务\"重试。", err)
				return
			}
			s.taskLbl.SetText("计划任务: " + state + " ✓")
			s.st.Log("settings: 计划任务修复成功, 状态=%s", state)
			showInfo(s.mw, "计划任务已修复, 并已确认注册成功(状态: %s)。", state)
		})
	}()
}

func (s *settings) collectServiceConfig() *config.Config {
	c := *s.cfg
	c.DirectMode = s.directCheck.Checked()
	c.DirectURL = strings.TrimSpace(s.directURL.Text())
	c.DirectToken = strings.TrimSpace(s.directTok.Text())
	c.IntervalHours = intervalValue(s.intervalCB.CurrentIndex())
	c.AskEnabled = s.askCk.Checked()
	// 自动换图开关与暂停状态: 关闭时按所选时长暂停, 到期自动恢复(防遗忘)
	c.AutoChange = s.autoCk.Checked()
	if c.AutoChange {
		c.PauseUntil = ""
		c.PauseHours = 0
	} else {
		c.PauseHours = s.pauseHours()
		c.PauseUntil = time.Now().Add(time.Duration(c.PauseHours) * time.Hour).Format(time.RFC3339)
	}
	return &c
}

// saveService 保存服务/调度表单; quiet 时不弹"已保存"提示(关闭窗口流程用)。
// 返回是否保存成功。
func (s *settings) saveService(quiet bool) bool {
	cfg := s.collectServiceConfig()
	*s.cfg = *cfg
	// 回访周期变化时从现在起算(后续满意仍沿记忆曲线自动上调)
	if days := int(s.askSpin.Value()); days != s.cfg.Satisfaction.IntervalDays {
		s.cfg.Satisfaction.IntervalDays = days
		s.cfg.Satisfaction.NextAskAt = time.Now().AddDate(0, 0, days).Format(time.RFC3339)
	}
	if err := s.cfg.Save(s.dir); err != nil {
		showError(s.mw, "保存失败: %v", err)
		return false
	}
	s.refreshAskLabel()
	s.be, s.cc = makeBackend(s.cfg)
	exe, err := os.Executable()
	if err == nil {
		// 统一安装名 bin\AIWallpaper.exe + 固定每小时计划任务 + 快捷方式/卸载项(幂等)
		if _, err := install.Setup(s.st.BinDir(), exe, update.CurrentVersion, s.cfg.PhaseMinutes); err != nil {
			s.st.Log("settings: 安装/任务注册失败: %v", err)
		} else {
			s.cfg.TaskTickHours = scheduler.TickHours
			_ = s.cfg.Save(s.dir)
		}
	}
	if !quiet {
		showInfo(s.mw, "设置已保存。")
	}
	return true
}

// serviceDirty 服务/调度页表单与已保存配置的差异(即"未保存修改")。
// 只比用户可编辑且由 saveService 落盘的字段; PauseUntil 为保存时按当前时间推导的动态值, 不参与比对。
func (s *settings) serviceDirty() bool {
	c := s.collectServiceConfig()
	cur := s.cfg
	if c.DirectMode != cur.DirectMode || c.DirectURL != cur.DirectURL || c.DirectToken != cur.DirectToken ||
		c.IntervalHours != cur.IntervalHours || c.AskEnabled != cur.AskEnabled || c.AutoChange != cur.AutoChange {
		return true
	}
	if int(s.askSpin.Value()) != cur.Satisfaction.IntervalDays {
		return true
	}
	if !c.AutoChange && c.PauseHours != cur.PauseHours {
		return true
	}
	return false
}

// confirmClose 标题栏 ✕ 关闭: 有未保存修改(偏好点选/服务表单)则询问,
// 选"是"保存后退出, "否"放弃修改退出, "取消"留在设置页; 无修改直接关闭退出。
func (s *settings) confirmClose() {
	profileDirty := s.answers != nil && !surveyEqual(s.answers, s.baseAnswers)
	svcDirty := s.serviceDirty()
	if profileDirty || svcDirty {
		var what []string
		if profileDirty {
			what = append(what, "偏好勾选")
		}
		if svcDirty {
			what = append(what, "服务与调度设置")
		}
		msg := "有未保存的修改: " + strings.Join(what, "、") + "\n保存后退出吗?\n\n" +
			"是 = 保存并退出\n否 = 放弃修改直接退出\n取消 = 留在设置页"
		switch walk.MsgBox(s.mw, config.AppNameCN, msg, walk.MsgBoxYesNoCancel|walk.MsgBoxIconQuestion) {
		case walk.DlgCmdYes:
			if profileDirty && !s.saveProfileEdits(true) {
				return // 保存失败: 留在设置页让用户处理
			}
			if svcDirty && !s.saveService(true) {
				return
			}
		case walk.DlgCmdNo:
			// 放弃修改直接退出
		default:
			return // 取消: 留在设置页
		}
	}
	s.mw.Close()
}

// removed: refreshTaskState (计划任务管理已从 UI 移除, 后台自动维护)

// ---------- 历史页 ----------

type wallpaperListModel struct {
	walk.ListModelBase
	names []string
	paths []string
}

func (m *wallpaperListModel) ItemCount() int { return len(m.names) }

func (m *wallpaperListModel) Value(index int) any { return m.names[index] }

func (s *settings) buildHistoryPage(tab *walk.TabWidget) error {
	page, err := newPage(tab, "历史")
	if err != nil {
		return err
	}
	row, _ := walk.NewComposite(page)
	_ = row.SetLayout(walk.NewHBoxLayout())

	left, _ := walk.NewComposite(row)
	_ = left.SetLayout(walk.NewVBoxLayout())
	s.histModel = &wallpaperListModel{}
	s.histLB, _ = walk.NewListBox(left)
	if err := s.histLB.SetModel(s.histModel); err != nil {
		return err
	}
	_ = s.histLB.SetMinMaxSize(walk.Size{Width: 320}, walk.Size{Width: 320})
	s.histLB.CurrentIndexChanged().Attach(func() { s.previewSelected() })

	right, _ := walk.NewComposite(row)
	_ = right.SetLayout(walk.NewVBoxLayout())
	s.histIV, _ = walk.NewImageView(right)
	_ = s.histIV.SetMinMaxSize(walk.Size{Width: 560, Height: 315}, walk.Size{Width: 560, Height: 315})

	btnRow, _ := walk.NewComposite(page)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	refBtn, _ := walk.NewPushButton(btnRow)
	refBtn.SetText("刷新")
	refBtn.Clicked().Attach(func() { s.reloadHistory() })
	applyBtn, _ := walk.NewPushButton(btnRow)
	applyBtn.SetText("重新应用这张")
	applyBtn.Clicked().Attach(func() { s.reapplySelected() })
	openBtn, _ := walk.NewPushButton(btnRow)
	openBtn.SetText("打开目录")
	openBtn.Clicked().Attach(func() { openFolder(s.st.WallpapersDir()) })

	s.reloadHistory()
	return nil
}

func (s *settings) reloadHistory() {
	entries, err := os.ReadDir(s.st.WallpapersDir())
	if err != nil {
		return
	}
	type item struct {
		name, path string
		mod        time.Time
	}
	var items []item
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "wallpaper_") || !strings.HasSuffix(e.Name(), ".png") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		items = append(items, item{e.Name(), filepath.Join(s.st.WallpapersDir(), e.Name()), info.ModTime()})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].mod.After(items[b].mod) })
	m := &wallpaperListModel{}
	for _, it := range items {
		m.names = append(m.names, fmt.Sprintf("%s  (%s)", it.name, it.mod.Format("01-02 15:04")))
		m.paths = append(m.paths, it.path)
	}
	_ = s.histLB.SetModel(m)
	s.histModel = m
}

func (s *settings) selectedPath() string {
	idx := s.histLB.CurrentIndex()
	if idx < 0 || idx >= len(s.histModel.paths) {
		return ""
	}
	return s.histModel.paths[idx]
}

func (s *settings) previewSelected() {
	path := s.selectedPath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if bmp, err := scalePlain(data, 560, 315); err == nil {
		s.histIV.SetImage(bmp)
	}
}

func (s *settings) reapplySelected() {
	path := s.selectedPath()
	if path == "" {
		showInfo(s.mw, "请先选择一张壁纸。")
		return
	}
	if err := desktop.SetWallpaper(path); err != nil {
		showError(s.mw, "应用失败: %v", err)
		return
	}
	s.st.SetCurrent(path)
	_ = signals.Append(s.dir, signals.Event{
		Type:   signals.TypeReapply,
		Detail: filepath.Base(path),
	})
	showInfo(s.mw, "已重新应用。该类画风会被记为正向证据, 影响后续出图。")
}

// ---------- 同步页 ----------

// removed: buildSyncPage (已并入设置页)

func (s *settings) createLink() {
	if s.cc == nil {
		showError(s.mw, "当前为直连调试模式, 无云端同步能力。")
		return
	}
	s.syncLbl.SetText("正在生成配对码...")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		resp, err := s.cc.LinkCreate(ctx)
		s.mw.Synchronize(func() {
			if err != nil {
				s.syncLbl.SetText("生成失败: " + err.Error())
				return
			}
			s.syncLbl.SetText(fmt.Sprintf("配对码 %s 已生成(5 分钟内有效)", resp.Code))
			showQRDialog(s.mw, resp.URL, resp.Code)
		})
	}()
}

func (s *settings) redeemLink() {
	if s.cc == nil {
		showError(s.mw, "当前为直连调试模式, 无云端同步能力。")
		return
	}
	code, ok := promptText(s.mw, "输入配对码", "请输入旧设备上显示的 6 位配对码:")
	if !ok || strings.TrimSpace(code) == "" {
		return
	}
	s.syncLbl.SetText("正在配对...")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		resp, err := s.cc.LinkRedeem(ctx, strings.TrimSpace(code))
		s.mw.Synchronize(func() {
			if err != nil {
				s.syncLbl.SetText("配对失败: " + err.Error())
				return
			}
			s.cfg.UserID = resp.UserID
			s.cfg.Token = resp.Token
			_ = s.cfg.Save(s.dir)
			_ = signals.Append(s.dir, signals.Event{Type: signals.TypeDeviceLinked, Detail: "新设备扫码配对成功"})
			s.userLbl.SetText("匿名用户 ID: " + s.cfg.UserID + "   (零注册零登录)")
			s.syncLbl.SetText("配对成功 ✓ 已领回原画像")
			s.be, s.cc = makeBackend(s.cfg)
		})
	}()
}

// ---------- 更新与回访页 ----------

// removed: buildUpdatePage (已并入设置页)

func (s *settings) refreshAskLabel() {
	next := s.cfg.Satisfaction.NextAskTime()
	if next.IsZero() {
		s.askLbl.SetText("偏好回访: 尚未安排")
		return
	}
	s.askLbl.SetText(fmt.Sprintf("偏好回访: 下次 %s (记忆曲线 3/7/15/30/60/90 天)",
		next.Format("2006-01-02 15:04")))
}

// releaseNotesURL "查看更新内容"跳转的 Gitee Releases 页(国内可达)。
const releaseNotesURL = "https://gitee.com/QQ941748/ai-wallpaper-workshop/releases"

// lastUpdateText 返回无感自更新的"最后更新时间"展示文本。
func (s *settings) lastUpdateText() string {
	if s.cfg.LastUpdate == "" {
		return "安装后自动记录"
	}
	if t, err := time.Parse(time.RFC3339, s.cfg.LastUpdate); err == nil {
		return t.Local().Format("2006-01-02 15:04")
	}
	return s.cfg.LastUpdate
}

// checkUpdateManual 手动检查更新: 查 manifest → 确认后下载+校验并预启动换装助手,
// 关闭窗口(进程退出)后由助手完成原子换装; 后台 tick 的自动无感更新不受影响。
func (s *settings) checkUpdateManual(btn *walk.PushButton) {
	btn.SetEnabled(false)
	btn.SetText("检查中…")
	restore := func() {
		s.mw.Synchronize(func() {
			if s.mw.IsDisposed() {
				return
			}
			btn.SetText("检查更新")
			btn.SetEnabled(true)
		})
	}
	exe, err := os.Executable()
	if err != nil {
		showError(s.mw, "无法定位程序路径: %v", err)
		restore()
		return
	}
	// 手动更新恒走云端 manifest(直连局域网模式也可更新)
	c := cloud.New(s.cfg.CloudURL, s.cfg.UserID, s.cfg.Token, hostName())
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		latest, err := c.ClientLatest(ctx, update.CurrentVersion)
		cancel()
		if err != nil {
			s.mw.Synchronize(func() { showError(s.mw, "查询更新失败: %v", err) })
			restore()
			return
		}
		if latest.Version == "" || update.CompareVersions(latest.Version, update.CurrentVersion) <= 0 {
			s.mw.Synchronize(func() {
				showInfo(s.mw, "当前已是最新版本 ("+update.CurrentVersion+")。")
			})
			restore()
			return
		}
		confirmed := false
		s.mw.Synchronize(func() {
			msg := fmt.Sprintf("发现新版本 %s (当前 %s)。\n\n是 = 立即下载更新(约 10 MB), 下载完成后关闭窗口自动换装, 下次打开即新版\n否 = 取消",
				latest.Version, update.CurrentVersion)
			confirmed = walk.MsgBox(s.mw, config.AppNameCN, msg, walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) == walk.DlgCmdYes
		})
		if !confirmed {
			s.st.Log("手动更新: 用户在确认框点了否/关闭")
			restore()
			return
		}
		s.st.Log("手动更新: 用户确认下载 %s (当前 %s)", latest.Version, update.CurrentVersion)
		s.mw.Synchronize(func() {
			if !s.mw.IsDisposed() {
				btn.SetText("下载中…")
			}
		})
		// 下载+校验+预启动换装助手(等待本进程退出后原子换装);
		// 进度回调按 200ms 节流刷按钮, 避免 64KB 级回调打爆 UI 线程
		var lastUI time.Time
		progressUI := func(got, total int64) {
			now := time.Now()
			if now.Sub(lastUI) < 200*time.Millisecond && (total <= 0 || got < total) {
				return
			}
			lastUI = now
			s.mw.Synchronize(func() {
				if s.mw.IsDisposed() {
					return
				}
				if total > 0 {
					btn.SetText(fmt.Sprintf("下载中 %d/%d MB", got>>20, total>>20))
				} else {
					btn.SetText(fmt.Sprintf("下载中 %d MB", got>>20))
				}
			})
		}
		dlCtx, dlCancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer dlCancel()
		if !update.Check(dlCtx, c, s.dir, exe, progressUI, s.st.Log) {
			s.st.Log("手动更新: 下载或校验未通过, 弹错误提示")
			s.mw.Synchronize(func() {
				showError(s.mw, "更新失败: 下载或校验未通过, 详见日志(数据目录 logs)。")
			})
			restore()
			return
		}
		s.st.Log("手动更新: 已暂存新版本 %s, 弹完成提示", latest.Version)
		// 走统一关闭流程(未保存修改会询问); 进程退出后助手完成换装
		s.mw.Synchronize(func() {
			walk.MsgBox(s.mw, config.AppNameCN,
				fmt.Sprintf("已更新到 %s，下次打开生效。\n\n确定后关闭窗口完成换装。", latest.Version),
				walk.MsgBoxOK|walk.MsgBoxIconInformation)
			s.confirmClose()
			if !s.mw.IsDisposed() {
				// 用户取消关闭: 换装助手等不到本进程退出会超时放弃, 后台 tick 下轮自动更新会重新下载完成换装
				btn.SetText("检查更新")
				btn.SetEnabled(true)
			}
		})
	}()
}

// ---------- 对话框助手 ----------

// promptText 简易文本输入对话框。
func promptText(owner walk.Form, title, label string) (string, bool) {
	dlg, err := walk.NewDialog(owner)
	if err != nil {
		return "", false
	}
	dlg.SetTitle(config.AppNameCN + " — " + title)
	dlg.SetSize(walk.Size{Width: 360, Height: 160})
	_ = dlg.SetLayout(walk.NewVBoxLayout())
	lbl, _ := walk.NewLabel(dlg)
	lbl.SetText(label)
	le, _ := walk.NewLineEdit(dlg)
	ok := false
	btnRow, _ := walk.NewComposite(dlg)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	okBtn, _ := walk.NewPushButton(btnRow)
	okBtn.SetText("确定")
	okBtn.Clicked().Attach(func() { ok = true; dlg.Accept() })
	cancelBtn, _ := walk.NewPushButton(btnRow)
	cancelBtn.SetText("取消")
	cancelBtn.Clicked().Attach(func() { dlg.Cancel() })
	dlg.Run()
	return le.Text(), ok
}

// showQRDialog 展示配对二维码与配对码。
func showQRDialog(owner walk.Form, pageURL, code string) {
	dlg, err := walk.NewDialog(owner)
	if err != nil {
		return
	}
	dlg.SetTitle(config.AppNameCN + " — 扫码配对")
	dlg.SetSize(walk.Size{Width: 360, Height: 460})
	_ = dlg.SetLayout(walk.NewVBoxLayout())

	if pngData, err := qrcode.Encode(pageURL, qrcode.Medium, 256); err == nil {
		if bmp, err := scalePlain(pngData, 256, 256); err == nil {
			iv, err := walk.NewImageView(dlg)
			if err == nil {
				iv.SetImage(bmp)
				_ = iv.SetMinMaxSize(walk.Size{Width: 256, Height: 256}, walk.Size{Width: 256, Height: 256})
			}
		}
	}
	codeLbl, _ := walk.NewLabel(dlg)
	codeLbl.SetText("配对码: " + code + "  (5 分钟内有效)")
	hint, _ := walk.NewLabel(dlg)
	hint.SetText("手机扫码打开中转页可转发配对码;\n或直接在新设备处输入上面的 6 位码。")
	closeBtn, _ := walk.NewPushButton(dlg)
	closeBtn.SetText("关闭")
	closeBtn.Clicked().Attach(func() { dlg.Accept() })
	dlg.Run()
}
