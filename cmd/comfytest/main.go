// cmd/comfytest 开发工具: 直连 ComfyUI, 用任意 API 格式工作流出图。
// 自动按 class_type 定位注入点(提示词/尺寸/seed), 提交后轮询下载, 可选设为桌面壁纸。
//
// 用法:
//
//	go run ./cmd/comfytest -wf d:\download\z_image_wallpaper.json -prompt "..." -w 1920 -h 1080 -out dist\z-lake.png -set
//	comfytest -apply dist\z-lake.png   # 完整换壁纸流程: 适配屏幕→留档→转码设壁纸→记录 current
package main

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"wallpaper/internal/config"
	"wallpaper/internal/desktop"
	"wallpaper/internal/store"
)

const (
	pollInterval = 3 * time.Second
	timeout      = 10 * time.Minute
)

type workflowNode struct {
	Inputs    map[string]any `json:"inputs"`
	ClassType string         `json:"class_type"`
}

func main() {
	wfPath := flag.String("wf", "", "API 格式工作流 JSON 路径(必填)")
	baseURL := flag.String("url", "http://127.0.0.1:8188", "ComfyUI 地址")
	prompt := flag.String("prompt", "", "正向提示词(注入到 CLIPTextEncode)")
	width := flag.Int("w", 1920, "出图宽度")
	height := flag.Int("h", 1080, "出图高度")
	out := flag.String("out", filepath.Join("dist", "z-lake.png"), "成品保存路径")
	setWP := flag.Bool("set", false, "出图成功后设为桌面壁纸")
	setOnly := flag.String("set-only", "", "仅将已有图片设为桌面壁纸(跳过生成, 验证换壁纸链路)")
	applyPath := flag.String("apply", "", "完整换壁纸流程: 适配屏幕→留档→转码设壁纸→记录 current")
	flag.Parse()

	log.SetFlags(0)
	logf := func(format string, args ...any) {
		log.Printf(time.Now().Format("2006-01-02 15:04:05")+" [comfytest] "+format, args...)
	}
	if *applyPath != "" {
		if err := applyWallpaper(*applyPath, logf); err != nil {
			logf("完整换壁纸失败: %v", err)
			os.Exit(1)
		}
		return
	}
	if *setOnly != "" {
		if err := desktop.SetWallpaper(*setOnly); err != nil {
			logf("设为壁纸失败: %v", err)
			os.Exit(1)
		}
		logf("已设为桌面壁纸: %s", *setOnly)
		return
	}
	if *wfPath == "" || *prompt == "" {
		logf("缺少参数: -wf 与 -prompt 必填")
		os.Exit(2)
	}

	start := time.Now()
	raw, err := os.ReadFile(*wfPath)
	if err != nil {
		logf("工作流读取失败: %v", err)
		os.Exit(1)
	}
	var wf map[string]workflowNode
	if err := json.Unmarshal(raw, &wf); err != nil {
		logf("工作流解析失败: %v", err)
		os.Exit(1)
	}
	logf("工作流加载: %s (%d 节点)", *wfPath, len(wf))

	textID, latentID, samplerID, saveID := findNodes(wf)
	if textID == "" || latentID == "" || samplerID == "" || saveID == "" {
		logf("节点定位失败: text=%s latent=%s sampler=%s save=%s", textID, latentID, samplerID, saveID)
		os.Exit(1)
	}

	// 注入: 提示词 / 尺寸 / 随机 seed
	wf[textID].Inputs["text"] = *prompt
	wf[latentID].Inputs["width"] = float64(*width)
	wf[latentID].Inputs["height"] = float64(*height)
	seed := randomSeed()
	wf[samplerID].Inputs["seed"] = seed
	logf("注入: 提示词→%s, 尺寸 %dx%d→%s, seed=%d→%s", textID, *width, *height, latentID, seed, samplerID)

	// 提交
	promptID, nodeErrs, err := submit(*baseURL, wf)
	if err != nil {
		logf("提交 /prompt 失败: %v", err)
		os.Exit(1)
	}
	if len(nodeErrs) > 0 {
		logf("ComfyUI 节点错误: %v(多为模型文件缺失, 请核对 models 目录文件名)", nodeErrs)
		os.Exit(1)
	}
	logf("已提交 prompt_id=%s (%.1fs)", promptID, time.Since(start).Seconds())

	// 轮询
	img, err := wait(*baseURL, promptID, saveID, logf)
	if err != nil {
		logf("出图失败: %v", err)
		os.Exit(1)
	}
	logf("出图完成 (%.1fs): %s", time.Since(start).Seconds(), img.Filename)

	// 下载
	data, err := download(*baseURL, img)
	if err != nil {
		logf("下载失败: %v", err)
		os.Exit(1)
	}
	if err := checkImage(data, *width, *height); err != nil {
		logf("成品自检未通过: %v(保留旧壁纸)", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		logf("输出目录创建失败: %v", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		logf("写文件失败: %v", err)
		os.Exit(1)
	}
	logf("已保存: %s (%.1fMB, %dx%d)", *out, float64(len(data))/1e6, *width, *height)

	if *setWP {
		if err := desktop.SetWallpaper(*out); err != nil {
			logf("设为壁纸失败: %v", err)
			os.Exit(1)
		}
		logf("已设为桌面壁纸")
	}
	logf("总耗时 %.1fs", time.Since(start).Seconds())
}

// findNodes 按 class_type 定位注入点(首个匹配)。
func findNodes(wf map[string]workflowNode) (textID, latentID, samplerID, saveID string) {
	for id, n := range wf {
		switch n.ClassType {
		case "CLIPTextEncode":
			if textID == "" {
				textID = id
			}
		case "EmptySD3LatentImage", "EmptyLatentImage":
			if latentID == "" {
				latentID = id
			}
		case "KSampler":
			if samplerID == "" {
				samplerID = id
			}
		case "SaveImage":
			if saveID == "" {
				saveID = id
			}
		}
	}
	return
}

func randomSeed() int64 {
	n, err := rand.Int(rand.Reader, big.NewInt(1<<62))
	if err != nil {
		return time.Now().UnixNano()
	}
	return n.Int64()
}

// applyWallpaper 完整换壁纸流程(与 tick 一致): 适配主屏 → 留档 → 转码设壁纸 → 记录 current。
func applyWallpaper(path string, logf func(string, ...any)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sw, sh := desktop.PrimarySize()
	final, err := desktop.FitToScreen(data, sw, sh)
	if err != nil {
		return fmt.Errorf("适配屏幕失败: %w", err)
	}
	logf("屏幕 %dx%d, 适配完成", sw, sh)
	st, err := store.Open(config.DefaultDir())
	if err != nil {
		return err
	}
	archived, err := st.SaveWallpaper(final, time.Now())
	if err != nil {
		return fmt.Errorf("留档失败: %w", err)
	}
	logf("已留档: %s", archived)
	if err := desktop.SetWallpaper(archived); err != nil {
		return fmt.Errorf("设为壁纸失败: %w", err)
	}
	st.SetCurrent(archived)
	logf("已设为桌面壁纸并记录 current")
	return nil
}

// submit POST /prompt, 返回 prompt_id 与 node_errors。
func submit(baseURL string, wf map[string]workflowNode) (string, map[string]any, error) {
	body, _ := json.Marshal(map[string]any{"prompt": wf})
	resp, err := http.Post(strings.TrimRight(baseURL, "/")+"/prompt", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw)))
	}
	var out struct {
		PromptID  string         `json:"prompt_id"`
		NodeErrs  map[string]any `json:"node_errors"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", nil, err
	}
	return out.PromptID, out.NodeErrs, nil
}

type outputImage struct {
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
	Type      string `json:"type"`
}

// wait 轮询 /history/{id} 直到 SaveImage 节点产出图片。
func wait(baseURL, promptID, saveID string, logf func(string, ...any)) (outputImage, error) {
	base := strings.TrimRight(baseURL, "/")
	deadline := time.Now().Add(timeout)
	poll := 1
	for time.Now().Before(deadline) {
		time.Sleep(pollInterval)
		resp, err := http.Get(base + "/history/" + url.PathEscape(promptID))
		if err == nil {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
			resp.Body.Close()
			var hist map[string]struct {
				Status struct {
					StatusStr string `json:"status_str"`
					Completed bool   `json:"completed"`
					Messages  []any  `json:"messages"`
				} `json:"status"`
				Outputs map[string]struct {
					Images []outputImage `json:"images"`
				} `json:"outputs"`
			}
			if err := json.Unmarshal(raw, &hist); err == nil {
				if entry, ok := hist[promptID]; ok {
					if entry.Status.StatusStr == "error" {
						return outputImage{}, fmt.Errorf("ComfyUI 执行错误: %v", entry.Status.Messages)
					}
					if entry.Status.Completed {
						if outs, ok := entry.Outputs[saveID]; ok && len(outs.Images) > 0 {
							return outs.Images[0], nil
						}
					}
					if poll == 1 || poll%10 == 0 {
						logf("轮询第 %d 次: status=%s", poll, entry.Status.StatusStr)
					}
				}
			}
		}
		poll++
	}
	return outputImage{}, fmt.Errorf("轮询超时(>%v)", timeout)
}

// download GET /view 取成品图。
func download(baseURL string, img outputImage) ([]byte, error) {
	q := url.Values{}
	q.Set("filename", img.Filename)
	q.Set("subfolder", img.Subfolder)
	q.Set("type", img.Type)
	resp, err := http.Get(strings.TrimRight(baseURL, "/") + "/view?" + q.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// checkImage 成品自检: PNG 可解码、尺寸吻合、非纯色。
func checkImage(data []byte, w, h int) error {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("不是可解码的图片: %v", err)
	}
	b := img.Bounds()
	if b.Dx() != w || b.Dy() != h {
		return fmt.Errorf("尺寸不符: 期望 %dx%d, 实际 %dx%d", w, h, b.Dx(), b.Dy())
	}
	first := img.At(b.Min.X, b.Min.Y)
	pts := [][2]int{
		{b.Min.X + b.Dx()/2, b.Min.Y + b.Dy()/2},
		{b.Max.X - 1, b.Max.Y - 1},
		{b.Min.X, b.Max.Y - 1},
	}
	same := 0
	for _, p := range pts {
		if colorAt(img, p[0], p[1]) == first {
			same++
		}
	}
	if same == len(pts) {
		return fmt.Errorf("疑似纯色/损坏图")
	}
	return nil
}

func colorAt(img image.Image, x, y int) color.Color {
	r, g, b, a := img.At(x, y).RGBA()
	return color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), uint8(a >> 8)}
}

func truncate(s string) string {
	if len(s) > 300 {
		return s[:300] + "..."
	}
	return s
}
