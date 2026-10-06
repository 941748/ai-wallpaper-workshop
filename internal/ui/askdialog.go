package ui

import (
	"os"
	"time"

	"github.com/lxn/walk"

	"wallpaper/internal/config"
	"wallpaper/internal/tick"
)

// askTimeout 回访小窗无响应时限: 超时按"稍后"处理, 绝不阻塞换壁纸主线。
const askTimeout = 120 * time.Second

// ShowAskDialog 满意度回访小窗(tick 到期时调用), 返回用户选择。
// 三个选项: 满意(周期+1天持续递增) / 换个风格(立即换+强漂移) / 稍后(顺延3天)。
func ShowAskDialog(req tick.AskRequest) tick.AskChoice {
	choice := tick.ChoiceLater
	dlg, err := walk.NewDialog(nil)
	if err != nil {
		return choice // 失败静默按稍后处理
	}
	dlg.SetTitle(config.AppNameCN + " — 偏好回访")
	dlg.SetSize(walk.Size{Width: 440, Height: 400})
	_ = dlg.SetLayout(walk.NewVBoxLayout())

	title, _ := walk.NewLabel(dlg)
	title.SetText("喜欢你当前的壁纸风格吗？")

	if data, err := os.ReadFile(req.ThumbPath); err == nil {
		if bmp, err := scalePlain(data, 380, 214); err == nil {
			iv, err := walk.NewImageView(dlg)
			if err == nil {
				iv.SetImage(bmp)
				_ = iv.SetMinMaxSize(walk.Size{Width: 380, Height: 214}, walk.Size{Width: 380, Height: 214})
			}
		}
	}

	hint, _ := walk.NewLabel(dlg)
	hint.SetText("选择\"满意\"后询问会越来越少; 选择\"换个风格\"会立即重生成一张不同风格的壁纸。")

	btnRow, _ := walk.NewComposite(dlg)
	_ = btnRow.SetLayout(walk.NewHBoxLayout())
	choose := func(c tick.AskChoice) func() {
		return func() {
			choice = c
			dlg.Accept()
		}
	}
	okBtn, _ := walk.NewPushButton(btnRow)
	okBtn.SetText("满意")
	okBtn.Clicked().Attach(choose(tick.ChoiceSatisfied))
	styleBtn, _ := walk.NewPushButton(btnRow)
	styleBtn.SetText("换个风格")
	styleBtn.Clicked().Attach(choose(tick.ChoiceStyle))
	laterBtn, _ := walk.NewPushButton(btnRow)
	laterBtn.SetText("稍后 (3 天后) ")
	laterBtn.Clicked().Attach(choose(tick.ChoiceLater))

	timer := time.AfterFunc(askTimeout, func() {
		dlg.Synchronize(func() { dlg.Accept() }) // 超时=稍后
	})
	dlg.Run()
	timer.Stop()
	return choice
}
