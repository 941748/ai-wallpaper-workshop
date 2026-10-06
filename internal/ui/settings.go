package ui

import (
	"context"
	"fmt"
	"os"
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
	"wallpaper/internal/probe"
	"wallpaper/internal/scheduler"
	"wallpaper/internal/signals"
	"wallpaper/internal/store"
	"wallpaper/internal/taxonomy"
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
	summaryLbl *walk.Label
	dimValues  [][]string
	combos     []*walk.ComboBox
	weights    []*walk.NumberEdit
	kwLB       *walk.ListBox
	kwEdit     *walk.LineEdit

	// 服务页
	urlEdit     *walk.LineEdit
	directCheck *walk.CheckBox
	directURL   *walk.LineEdit
	directTok   *walk.LineEdit
	directRow   *walk.Composite
	connLbl     *walk.Label
	intervalCB  *walk.ComboBox
	quietCheck  *walk.CheckBox
	quietStart  *walk.LineEdit
	quietEnd    *walk.LineEdit
	taskLbl     *walk.Label

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
	mw, err := walk.NewMainWindow()
	if err != nil {
		return err
	}
	s.mw = mw
	mw.SetTitle(config.AppNameCN + " — 设置")
	mw.SetSize(fitWorkArea(mw, walk.Size{Width: 980, Height: 680}))
	mw.SetMinMaxSize(fitWorkArea(mw, walk.Size{Width: 860, Height: 560}), walk.Size{})
	_ = mw.SetLayout(walk.NewVBoxLayout())

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
	if err := s.buildServicePage(tab); err != nil {
		return err
	}
	if err := s.buildHistoryPage(tab); err != nil {
		return err
	}
	if err := s.buildSyncPage(tab); err != nil {
		return err
	}
	if err := s.buildUpdatePage(tab); err != nil {
		return err
	}
	return nil
}

func newPage(tab *walk.TabWidget, title string) (*walk.TabPage, error) {
	page, err := walk.NewTabPage()
	if err != nil {
		return nil, err
	}
	page.SetTitle(title)
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
	s.summaryLbl, _ = walk.NewLabel(page)
	s.refreshSummary(profile)
	hint, _ := walk.NewLabel(page)
	hint.SetText("提示: 这里只调各维主值; 要重新勾选完整偏好清单(风格/题材等全部选项), 点下方\"重新初始化向导\"。")

	for _, d := range taxonomy.Dimensions {
		ids := make([]string, 0, len(d.Values))
		for _, v := range d.Values {
			ids = append(ids, v.ID)
		}
		sort.SliceStable(ids, func(a, b int) bool {
			return profile.Get(d.ID, ids[a]) > profile.Get(d.ID, ids[b])
		})
		names := make([]string, len(ids))
		for i, id := range ids {
			names[i] = taxonomy.NameCN(d.ID, id)
		}
		s.dimValues = append(s.dimValues, ids)

		row, _ := walk.NewComposite(page)
		_ = row.SetLayout(walk.NewHBoxLayout())
		lbl, _ := walk.NewLabel(row)
		lbl.SetText(d.NameCN + ":")
		cb, _ := walk.NewComboBox(row)
		_ = cb.SetModel(names)
		cb.SetCurrentIndex(0)
		ne, _ := walk.NewNumberEdit(row)
		ne.SetDecimals(2)
		ne.SetRange(0.05, 1.0)
		ne.SetValue(profile.Get(d.ID, ids[0]))
		s.combos = append(s.combos, cb)
		s.weights = append(s.weights, ne)
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

	btnRow, _ := walk.NewComposite(page)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	saveBtn, _ := walk.NewPushButton(btnRow)
	saveBtn.SetText("保存画像")
	saveBtn.Clicked().Attach(func() { s.saveProfileEdits() })
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
	s.refreshSummary(profile)
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
	swapBtn.Clicked().Attach(func() {
		if err := scheduler.RunNow(); err != nil {
			showError(s.mw, "%v", err)
		} else {
			s.curLbl.SetText("已触发换图, 稍后桌面会自动刷新(可重开本窗口查看)")
		}
	})

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

// refreshSummary 刷新画像摘要标签。
func (s *settings) refreshSummary(profile *config.Profile) {
	items := probe.Summarize(profile, 10)
	if len(items) == 0 {
		s.summaryLbl.SetText("画像摘要: 暂无(多为中立)")
	} else {
		s.summaryLbl.SetText("画像摘要: " + strings.Join(items, " · "))
	}
}

func (s *settings) saveProfileEdits() {
	profile, _ := config.LoadProfile(s.dir)
	for i, d := range taxonomy.Dimensions {
		idx := s.combos[i].CurrentIndex()
		if idx < 0 || idx >= len(s.dimValues[i]) {
			idx = 0
		}
		profile.Set(d.ID, s.dimValues[i][idx], s.weights[i].Value())
	}
	if err := profile.Save(s.dir); err != nil {
		showError(s.mw, "画像保存失败: %v", err)
		return
	}
	s.cfg.ProfileVer++
	_ = s.cfg.Save(s.dir)
	_ = signals.Append(s.dir, signals.Event{Type: signals.TypeWeightAdjust, Detail: "设置页手工调整偏好权重"})
	s.refreshSummary(profile)
	showInfo(s.mw, "画像已保存。云端 LLM 将参考你的调整生成后续壁纸。")
}

// ---------- 服务与调度页 ----------

func (s *settings) buildServicePage(tab *walk.TabWidget) error {
	page, err := newPage(tab, "服务与调度")
	if err != nil {
		return err
	}
	s.urlEdit, _ = addLineRow(page, "云端服务地址:", s.cfg.CloudURL, 420)

	s.directCheck, _ = walk.NewCheckBox(page)
	s.directCheck.SetText("调试模式: 直连局域网 ComfyUI(高级, 默认关闭)")
	s.directCheck.SetChecked(s.cfg.DirectMode)
	s.directRow, _ = walk.NewComposite(page)
	_ = s.directRow.SetLayout(walk.NewVBoxLayout())
	s.directURL, _ = addLineRow(s.directRow, "ComfyUI 地址:", s.cfg.DirectURL, 420)
	s.directTok, _ = addLineRow(s.directRow, "访问令牌(可选):", s.cfg.DirectToken, 420)
	s.directRow.SetVisible(s.cfg.DirectMode)
	s.directCheck.CheckedChanged().Attach(func() { s.directRow.SetVisible(s.directCheck.Checked()) })

	btnRow, _ := walk.NewComposite(page)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	saveBtn, _ := walk.NewPushButton(btnRow)
	saveBtn.SetText("保存并修复任务")
	saveBtn.Clicked().Attach(func() { s.saveService() })
	testBtn, _ := walk.NewPushButton(btnRow)
	testBtn.SetText("测试连接")
	testBtn.Clicked().Attach(func() {
		s.connLbl.SetText("测试中...")
		go func() {
			err := testConnection(s.collectServiceConfig())
			s.mw.Synchronize(func() {
				if err != nil {
					s.connLbl.SetText("连接失败: " + err.Error())
				} else {
					s.connLbl.SetText("连接成功 ✓")
				}
			})
		}()
	})
	s.connLbl, _ = walk.NewLabel(page)

	// 换图间隔
	row1, _ := walk.NewComposite(page)
	_ = row1.SetLayout(walk.NewHBoxLayout())
	lbl1, _ := walk.NewLabel(row1)
	lbl1.SetText("换图间隔:")
	s.intervalCB, _ = walk.NewComboBox(row1)
	_ = s.intervalCB.SetModel([]string{"1 小时", "2 小时", "3 小时", "4 小时", "6 小时", "8 小时", "12 小时"})
	_ = s.intervalCB.SetCurrentIndex(intervalIndex(s.cfg.IntervalHours))

	// 安静模式
	row2, _ := walk.NewComposite(page)
	_ = row2.SetLayout(walk.NewHBoxLayout())
	s.quietCheck, _ = walk.NewCheckBox(row2)
	s.quietCheck.SetText("安静模式(全屏/静默时段不打扰)")
	s.quietCheck.SetChecked(s.cfg.QuietEnabled)
	lbl2, _ := walk.NewLabel(row2)
	lbl2.SetText("  静默时段:")
	s.quietStart, _ = walk.NewLineEdit(row2)
	s.quietStart.SetText(s.cfg.QuietStart)
	_ = s.quietStart.SetMinMaxSize(walk.Size{Width: 64}, walk.Size{Width: 64})
	toLbl, _ := walk.NewLabel(row2)
	toLbl.SetText("—")
	s.quietEnd, _ = walk.NewLineEdit(row2)
	s.quietEnd.SetText(s.cfg.QuietEnd)
	_ = s.quietEnd.SetMinMaxSize(walk.Size{Width: 64}, walk.Size{Width: 64})

	// 任务状态
	taskRow, _ := walk.NewComposite(page)
	_ = taskRow.SetLayout(walk.NewHBoxLayout())
	s.taskLbl, _ = walk.NewLabel(taskRow)
	s.refreshTaskState()
	refreshBtn, _ := walk.NewPushButton(taskRow)
	refreshBtn.SetText("刷新状态")
	refreshBtn.Clicked().Attach(func() { s.refreshTaskState() })
	runBtn, _ := walk.NewPushButton(taskRow)
	runBtn.SetText("立即换一张")
	runBtn.Clicked().Attach(func() {
		if err := scheduler.RunNow(); err != nil {
			showError(s.mw, "%v", err)
		} else {
			showInfo(s.mw, "已触发一次换图, 稍后桌面壁纸会自动刷新。")
		}
	})
	delBtn, _ := walk.NewPushButton(taskRow)
	delBtn.SetText("删除任务")
	delBtn.Clicked().Attach(func() {
		if walk.MsgBox(s.mw, config.AppNameCN, "删除后不再自动换壁纸, 确认删除?",
			walk.MsgBoxYesNo|walk.MsgBoxIconWarning) == walk.DlgCmdYes {
			if err := scheduler.Delete(); err != nil {
				showError(s.mw, "%v", err)
			}
			s.refreshTaskState()
		}
	})
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

func (s *settings) collectServiceConfig() *config.Config {
	c := *s.cfg
	c.CloudURL = strings.TrimSpace(s.urlEdit.Text())
	c.DirectMode = s.directCheck.Checked()
	c.DirectURL = strings.TrimSpace(s.directURL.Text())
	c.DirectToken = strings.TrimSpace(s.directTok.Text())
	c.IntervalHours = intervalValue(s.intervalCB.CurrentIndex())
	c.QuietEnabled = s.quietCheck.Checked()
	c.QuietStart = strings.TrimSpace(s.quietStart.Text())
	c.QuietEnd = strings.TrimSpace(s.quietEnd.Text())
	return &c
}

func (s *settings) saveService() {
	cfg := s.collectServiceConfig()
	*s.cfg = *cfg
	if err := s.cfg.Save(s.dir); err != nil {
		showError(s.mw, "保存失败: %v", err)
		return
	}
	s.be, s.cc = makeBackend(s.cfg)
	exe, err := os.Executable()
	if err == nil {
		dst := filepath.Join(s.st.BinDir(), "wallpaper.exe")
		if !samePath(exe, dst) {
			if err := copyFile(exe, dst); err != nil {
				showError(s.mw, "复制程序失败: %v", err)
			}
		}
		if err := scheduler.Register(dst, s.cfg.IntervalHours, s.cfg.PhaseMinutes); err != nil {
			showError(s.mw, "任务注册失败: %v", err)
		} else {
			showInfo(s.mw, "设置已保存, 计划任务已修复。")
		}
	}
	s.refreshTaskState()
}

func (s *settings) refreshTaskState() {
	state, err := scheduler.QueryState()
	if err != nil || state == "" {
		s.taskLbl.SetText("计划任务: 未注册(点击\"保存并修复任务\"注册)")
		return
	}
	s.taskLbl.SetText("计划任务: " + state)
}

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

func (s *settings) buildSyncPage(tab *walk.TabWidget) error {
	page, err := newPage(tab, "同步")
	if err != nil {
		return err
	}
	s.userLbl, _ = walk.NewLabel(page)
	s.userLbl.SetText("匿名用户 ID: " + s.cfg.UserID + "   (零注册零登录)")
	s.syncLbl, _ = walk.NewLabel(page)
	s.syncLbl.SetText("同步状态: 正常(每轮出图自动同步)")

	btnRow, _ := walk.NewComposite(page)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	qrBtn, _ := walk.NewPushButton(btnRow)
	qrBtn.SetText("生成配对二维码 (迁到新设备)")
	qrBtn.Clicked().Attach(func() { s.createLink() })
	redeemBtn, _ := walk.NewPushButton(btnRow)
	redeemBtn.SetText("输入配对码 (从旧设备恢复)")
	redeemBtn.Clicked().Attach(func() { s.redeemLink() })

	hint, _ := walk.NewLabel(page)
	hint.SetText("说明: 换机/重装时, 在旧设备生成二维码,\n" +
		"用手机扫码打开中转页(或直接读取配对码), 在新设备输入 6 位配对码即可领回同一画像;\n" +
		"无需注册账号, 云端数据不会清除。")
	return nil
}

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

func (s *settings) buildUpdatePage(tab *walk.TabWidget) error {
	page, err := newPage(tab, "更新与回访")
	if err != nil {
		return err
	}
	s.versionLbl, _ = walk.NewLabel(page)
	s.versionLbl.SetText("当前版本: " + update.CurrentVersion)

	btnRow, _ := walk.NewComposite(page)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	checkBtn, _ := walk.NewPushButton(btnRow)
	checkBtn.SetText("检查更新")
	checkBtn.Clicked().Attach(func() { s.checkUpdate() })
	s.updateLbl, _ = walk.NewLabel(btnRow)
	s.updateLbl.SetText("")

	askRow, _ := walk.NewComposite(page)
	_ = askRow.SetLayout(walk.NewHBoxLayout())
	s.askLbl, _ = walk.NewLabel(askRow)
	s.refreshAskLabel()
	lbl, _ := walk.NewLabel(askRow)
	lbl.SetText("  回访周期(天):")
	s.askSpin, _ = walk.NewNumberEdit(askRow)
	s.askSpin.SetDecimals(0)
	s.askSpin.SetRange(1, 365)
	s.askSpin.SetValue(float64(s.cfg.Satisfaction.IntervalDays))
	saveBtn, _ := walk.NewPushButton(askRow)
	saveBtn.SetText("保存")
	saveBtn.Clicked().Attach(func() {
		s.cfg.Satisfaction.IntervalDays = int(s.askSpin.Value())
		// 手动周期直接生效(从现在起算), 后续满意仍沿曲线阶梯上调
		s.cfg.Satisfaction.NextAskAt = time.Now().AddDate(0, 0, s.cfg.Satisfaction.IntervalDays).Format(time.RFC3339)
		if err := s.cfg.Save(s.dir); err != nil {
			showError(s.mw, "保存失败: %v", err)
			return
		}
		s.refreshAskLabel()
		showInfo(s.mw, "回访周期已保存。")
	})

	openBtn, _ := walk.NewPushButton(page)
	openBtn.SetText("打开数据目录 (config/profile/日志/壁纸)")
	openBtn.Clicked().Attach(func() { openFolder(s.dir) })
	return nil
}

func (s *settings) refreshAskLabel() {
	next := s.cfg.Satisfaction.NextAskTime()
	if next.IsZero() {
		s.askLbl.SetText("偏好回访: 尚未安排")
		return
	}
	s.askLbl.SetText(fmt.Sprintf("偏好回访: 下次 %s (记忆曲线 3/7/15/30/60/90 天)",
		next.Format("2006-01-02 15:04")))
}

func (s *settings) checkUpdate() {
	s.updateLbl.SetText("检查中...")
	go func() {
		var got bool
		if s.cc == nil {
			s.mw.Synchronize(func() { s.updateLbl.SetText("直连调试模式不支持云端更新") })
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		exe, _ := os.Executable()
		got = update.Check(ctx, s.cc, s.dir, exe, func(string, ...any) {})
		s.mw.Synchronize(func() {
			if got {
				s.updateLbl.SetText("发现新版本, 已暂存, 退出后将自动换装 ✓")
			} else {
				s.updateLbl.SetText("已是最新版本 (或检查失败, 稍后自动重试)")
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
