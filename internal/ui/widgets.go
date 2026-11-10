// Package ui 共享工具: 错误文案脱敏、图片缩放、系统调用
// (满意度回访/卸载确认等 walk 对话框与前端桥方法共用)。
// 设计约束: 关闭窗口 = 进程立即退出; 无托盘、无后台服务、无 Web 界面。
package ui

import (
	"bytes"
	"image"
	"image/draw"
	"image/png"
	"os/exec"
	"strings"

	"github.com/lxn/walk"

	imgdraw "golang.org/x/image/draw"
)

// friendlyErrText 将底层错误转为面向用户的简短提示: 含地址/路径等技术细节的错误不外露
// (如 ComfyUI/云端地址、URL、网络栈原文); 已是友好文案的错误(如 "云端未就绪")原样返回。
// 完整错误应由调用方写入日志(runtime.log)便于诊断。
func friendlyErrText(err error) string {
	if err == nil {
		return "未知错误"
	}
	return friendlyText(err.Error())
}

// friendlyText 同 friendlyErrText, 但接收字符串(用于 "阶段前缀: 错误原文" 形式的拼接文案)。
// 前缀(如 "出图失败")保留, 仅对技术细节部分做替换。
func friendlyText(msg string) string {
	if !looksTechnical(msg) {
		return msg
	}
	low := strings.ToLower(msg)
	var cls string
	switch {
	case strings.Contains(low, "timeout") || strings.Contains(low, "deadline"):
		cls = "网络连接超时"
	case strings.Contains(low, "refused") || strings.Contains(low, "unreachable") || strings.Contains(low, "no route"):
		cls = "服务暂时不可达"
	case strings.Contains(low, "no such host") || strings.Contains(low, "dns"):
		cls = "网络解析失败"
	case strings.Contains(low, "eof") || strings.Contains(low, "reset"):
		cls = "连接被中断"
	default:
		cls = "暂时无法连接"
	}
	// "阶段前缀: 详情" 结构保留前缀(如 "出图失败"), 前缀含路径/URL 字符时不保留
	if i := strings.Index(msg, ": "); i > 0 && i < 20 && !strings.ContainsAny(msg[:i], "/\\") {
		return msg[:i] + ": " + cls
	}
	return cls
}

// looksTechnical 判断错误文本是否含 URL/路径/网络栈等不宜直接展示的技术细节。
func looksTechnical(s string) bool {
	low := strings.ToLower(s)
	for _, k := range []string{"://", "http", "dial ", "tcp", "connect", "deadline", "no such host", "eof", "\\"} {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

// decodePNG 解码 PNG 字节。
func decodePNG(data []byte) (image.Image, error) {
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return img, nil
}

// scalePlain 缩放图片到指定尺寸(无边框)。
func scalePlain(data []byte, maxW, maxH int) (*walk.Bitmap, error) {
	src, err := decodePNG(data)
	if err != nil {
		return nil, err
	}
	sb := src.Bounds()
	w, h := maxW, maxH
	if sb.Dx() > 0 && sb.Dy() > 0 {
		scale := float64(maxW) / float64(sb.Dx())
		if s := float64(maxH) / float64(sb.Dy()); s < scale {
			scale = s
		}
		w = int(float64(sb.Dx()) * scale)
		h = int(float64(sb.Dy()) * scale)
	}
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	imgdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, draw.Over, nil)
	return walk.NewBitmapFromImage(dst)
}

// openURL 用系统默认浏览器打开链接(无控制台窗口)。
func openURL(url string) {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}
