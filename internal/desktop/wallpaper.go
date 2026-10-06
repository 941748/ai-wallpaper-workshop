package desktop

import (
	"fmt"
	"image"
	_ "image/jpeg" // 注册 JPEG 解码器(转码来源)
	_ "image/png"  // 注册 PNG 解码器(转码来源)
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	"golang.org/x/sys/windows"
)

// SetWallpaper 设置桌面壁纸(SPI_SETDESKWALLPAPER, 写入用户配置并广播刷新)。
// 注意: SPI 仅可靠支持 BMP, PNG/JPG 会静默失效(返回成功但壁纸不变);
// 因此非 BMP 输入先转码为同目录 current.bmp 再设置, 注册表始终引用该稳定文件。
func SetWallpaper(path string) error {
	use := path
	var err error
	if !strings.EqualFold(filepath.Ext(path), ".bmp") {
		use, err = toBMP(path)
		if err != nil {
			return err
		}
	}
	// 注册表需存绝对路径(相对路径重启后无法解析)。
	use, err = filepath.Abs(use)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(use)
	if err != nil {
		return err
	}
	r1, _, errno := procSystemParametersInfoW.Call(
		spiSetDeskWallpaper,
		0,
		uintptr(unsafe.Pointer(p)),
		spifUpdateINIFile|spifSendWinIniChange,
	)
	if r1 == 0 {
		return fmt.Errorf("SystemParametersInfoW 换壁纸失败: %v", errno)
	}
	return nil
}

// toBMP 将任意可解码图片转码为同目录 current.bmp 并返回其路径(临时文件原子替换)。
func toBMP(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return "", fmt.Errorf("解码图片 %s: %w", filepath.Base(path), err)
	}
	// BMP 编码器仅支持具体像素类型, 归一化为 RGBA。
	dst := image.NewRGBA(img.Bounds())
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Src)
	out := filepath.Join(filepath.Dir(path), "current.bmp")
	tmp := out + ".tmp"
	w, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	if err := bmp.Encode(w, dst); err != nil {
		_ = w.Close()
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, out); err != nil {
		return "", err
	}
	return out, nil
}
