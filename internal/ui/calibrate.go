package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/lxn/walk"

	"wallpaper/internal/config"
	"wallpaper/internal/probe"
	"wallpaper/internal/signals"
	"wallpaper/internal/taxonomy"
)

// runProbeCalibration 打开"图片探针校准"对话框(设置页偏好页触发)。
// 流程: 生成 12 张不同风格的探针图 → 用户逐张点选(中立/喜欢/不喜欢) →
// probe.Infer 与当前画像融合(0.65/0.35, 微调不推翻) → 落盘并递增 ProfileVer →
// 结果反推回偏好页的三态勾选状态。作为偏好微调手段, 不必重跑初始化向导。
func (s *settings) runProbeCalibration() {
	dlg, err := walk.NewDialog(s.mw)
	if err != nil {
		showError(s.mw, "校准窗口创建失败: %v", err)
		return
	}
	initTheme()
	dlg.SetTitle(config.AppNameCN + " — 图片探针校准")
	if icon, err := walk.NewIconFromResourceId(2); err == nil {
		_ = dlg.SetIcon(icon)
	}
	dlg.SetBackground(brushPageBg)
	dlg.SetSize(fitWorkArea(dlg, walk.Size{Width: 1040, Height: 640}))
	dlg.SetMinMaxSize(fitWorkArea(dlg, walk.Size{Width: 900, Height: 520}), walk.Size{})
	_ = dlg.SetLayout(walk.NewVBoxLayout())

	themedLabel(dlg, "图片探针校准", themeFontStep, colTitle)
	themedLabel(dlg, "生成 12 张不同风格的探针图, 点选切换: 中立 → ✓ 喜欢 → ✕ 不喜欢; 至少 3 张\"喜欢\"后可完成校准。\n"+
		"校准结果与你当前偏好融合(微调, 不推翻), 让后续出图更贴近你。", nil, colHint)

	statusLbl := themedLabel(dlg, "", nil, colHint)
	progress, _ := walk.NewProgressBar(dlg)
	progress.SetRange(0, 100)

	content, _ := walk.NewComposite(dlg)
	_ = content.SetLayout(walk.NewVBoxLayout())
	if bl, ok := dlg.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(content, 1)
	}

	btnRow, _ := walk.NewComposite(dlg)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	hsp, _ := walk.NewHSpacer(btnRow)
	cancelBtn, _ := walk.NewPushButton(btnRow)
	cancelBtn.SetText("取消")
	cancelBtn.Clicked().Attach(func() { dlg.Cancel() })
	doneBtn, _ := walk.NewPushButton(btnRow)
	doneBtn.SetText("完成校准")
	doneBtn.SetEnabled(false)
	if bl, ok := btnRow.Layout().(*walk.BoxLayout); ok {
		_ = bl.SetStretchFactor(hsp, 1)
	}

	combos := probe.SurveyCombos(probe.FirstCount)
	var grid *probeGrid
	closed := false // 对话框已关闭: 阻止在途回调触碰已销毁的控件

	doneBtn.Clicked().Attach(func() {
		if grid == nil || grid.Likes() < 3 {
			return
		}
		s.applyCalibration(combos, grid.Choices())
		dlg.Accept()
	})

	statusLbl.SetText("正在生成 12 张探针图(约 1-2 分钟)...")
	progress.SetValue(0)

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		imgs := acquireProbeImages(ctx, s.cfg, s.cc, s.be, combos, func(done, total int) {
			s.mw.Synchronize(func() {
				if closed {
					return
				}
				progress.SetValue(done * 100 / maxInt(total, 1))
				statusLbl.SetText(fmt.Sprintf("已完成 %d / %d", done, total))
			})
		})
		// 落盘留档(便于排查与复用)
		for _, cb := range combos {
			if data, ok := imgs[cb.ID]; ok {
				_ = os.WriteFile(filepath.Join(s.st.ProbesDir(), cb.ID+".png"), data, 0o644)
			}
		}
		s.mw.Synchronize(func() {
			if closed {
				return
			}
			if len(imgs) == 0 {
				progress.SetVisible(false)
				statusLbl.SetTextColor(colRed)
				statusLbl.SetText("探针生成失败(出图服务不可用), 请检查连接后重试。")
				return
			}
			clearChildren(content)
			g, gerr := newProbeGrid(content, combos, imgs, func() {
				doneBtn.SetEnabled(grid != nil && grid.Likes() >= 3)
			})
			if gerr != nil {
				statusLbl.SetTextColor(colRed)
				statusLbl.SetText("探针网格创建失败: " + gerr.Error())
				return
			}
			grid = g
			progress.SetVisible(false)
			statusLbl.SetTextColor(colHint)
			if len(imgs) < len(combos) {
				statusLbl.SetText(fmt.Sprintf("生成完成 %d / %d(失败的可跳过); 点选后完成校准", len(imgs), len(combos)))
			} else {
				statusLbl.SetText("点选图片切换: 中立 → ✓ 喜欢 → ✕ 不喜欢; 至少 3 张\"喜欢\"后点\"完成校准\"")
			}
		})
	}()

	dlg.Run()
	closed = true
}

// applyCalibration 融合探针点选结果到画像并落盘, 反推回偏好页勾选状态。
func (s *settings) applyCalibration(combos []probe.Combo, choices map[string]probe.Choice) {
	prev, _ := config.LoadProfile(s.dir)
	merged := probe.Infer(combos, choices, prev)
	// Infer 重建画像会丢失自定义关键词, 需从旧画像保留
	if prev != nil {
		merged.CustomKeywords = append([]string{}, prev.CustomKeywords...)
	}
	if err := merged.Save(s.dir); err != nil {
		showError(s.mw, "校准结果保存失败: %v", err)
		return
	}
	s.cfg.ProfileVer++
	_ = s.cfg.Save(s.dir)
	likes := 0
	for _, c := range choices {
		if c == probe.Like {
			likes++
		}
	}
	_ = signals.Append(s.dir, signals.Event{
		Type:   signals.TypeReconfigure,
		Detail: fmt.Sprintf("设置页图片探针校准: 喜欢 %d 项", likes),
	})
	// 反推回偏好页三态勾选状态
	for _, d := range taxonomy.Dimensions {
		for _, v := range d.Values {
			s.answers[config.Key(d.ID, v.ID)] = attitudeOfWeight(merged.Get(d.ID, v.ID))
		}
	}
	if s.chips != nil {
		s.chips.refresh()
	}
	s.refreshSummary(merged)
	showInfo(s.mw, "校准完成: 已融合你的点选结果, 后续出图将更贴近你的偏好。")
}
