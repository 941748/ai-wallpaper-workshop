package ui

import (
	"os"
	"os/exec"
	"time"

	"github.com/lxn/walk"

	"wallpaper/internal/config"
	"wallpaper/internal/tick"
)

// askTimeout 回访小窗无响应时限: 超时按"沉默"处理(曲线拉长, 不追问), 绝不阻塞换壁纸主线。
const askTimeout = 120 * time.Second

// ShowAskDialog 满意度回访小窗(tick 到期时调用), 返回用户选择。
// 四个选项: 满意(曲线拉长) / 换个风格(立即换+强漂移) / 调整一下(打开设置) / 以后再说(顺延3天)。
func ShowAskDialog(req tick.AskRequest) tick.AskChoice {
	choice := tick.ChoiceLater
	dlg, err := walk.NewDialog(nil)
	if err != nil {
		return choice // 失败静默按稍后处理
	}
	dlg.SetTitle(config.AppNameCN + " — 偏好回访")
	// 标题栏图标: 从 exe 资源加载(rsrc 嵌入的 RT_GROUP_ICON id=2; manifest 占用 id=1)
	if icon, err := walk.NewIconFromResourceId(2); err == nil {
		_ = dlg.SetIcon(icon)
	}
	dlg.SetBackground(brushPageBg)
	dlg.SetSize(walk.Size{Width: 440, Height: 400})
	_ = dlg.SetLayout(walk.NewVBoxLayout())

	title, _ := walk.NewLabel(dlg)
	title.SetText("最近的壁纸, 还合你心意吗？")

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
	hint.SetText("满意后询问会越来越少(3→7→15→30→60→90 天); \"调整一下\"可直接打开设置微调偏好。")

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
	adjustBtn, _ := walk.NewPushButton(btnRow)
	adjustBtn.SetText("调整一下")
	adjustBtn.Clicked().Attach(func() {
		// 打开设置面板让用户重新定义方向(启动自身 exe, 无参数即设置窗口)
		if exe, err := os.Executable(); err == nil {
			_ = exec.Command(exe).Start()
		}
		choice = tick.ChoiceAdjust
		dlg.Accept()
	})
	laterBtn, _ := walk.NewPushButton(btnRow)
	laterBtn.SetText("以后再说")
	laterBtn.Clicked().Attach(choose(tick.ChoiceLater))

	timer := time.AfterFunc(askTimeout, func() {
		dlg.Synchronize(func() {
			choice = tick.ChoiceTimeout // 超时=沉默: 曲线拉长
			dlg.Accept()
		})
	})
	dlg.Run()
	timer.Stop()
	return choice
}
