// Command aw-uploader 出图机本地上传代理(P1 OSS 直传)。
//
// 职责只有一条: 服务端列出"渲染完成待上传"的任务 → 从本机 ComfyUI 取字节 →
// 直传阿里云 OSS(国内链路, 不经过新加坡服务器) → 回报服务端标记完成。
// 服务端在 30 秒内收不到回报时会自动回退隧道拉图(现网老路径), 本程序挂掉不影响出图链路。
//
// 配置: aw-uploader.json(与本 exe 同目录, 由分发包预填)。
// 运行: 双击 运行aw-uploader.bat(或直接运行 exe, 窗口保持打开)。
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // ComfyUI 出图为 PNG
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ossConf struct {
	AkID     string `json:"ak_id"`
	AkSecret string `json:"ak_secret"`
	Bucket   string `json:"bucket"`
	Endpoint string `json:"endpoint"`
}

type config struct {
	Server      string  `json:"server"`
	AgentToken  string  `json:"agent_token"`
	Oss         ossConf `json:"oss"`
	ComfyPorts  []int   `json:"comfy_ports"`
	PollSeconds int     `json:"poll_seconds"`
}

type task struct {
	JobID     string `json:"job_id"`
	OssKey    string `json:"oss_key"`
	Filename  string `json:"filename"`
	Subfolder string `json:"subfolder"`
}

var (
	cfg       config
	comfyBase string
	httpc     = &http.Client{Timeout: 60 * time.Second}
)

func main() {
	log.SetFlags(log.Ltime)
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("定位自身路径失败: %v", err)
	}
	cfgPath := filepath.Join(filepath.Dir(exe), "aw-uploader.json")
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		log.Fatalf("读取配置失败(%s): %v", cfgPath, err)
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		log.Fatalf("配置解析失败: %v", err)
	}
	if cfg.PollSeconds <= 0 {
		cfg.PollSeconds = 5
	}
	log.Printf("服务端 %s | OSS %s/%s", cfg.Server, cfg.Oss.Bucket, cfg.Oss.Endpoint)

	for {
		if comfyBase == "" || time.Since(lastProbe) > time.Minute {
			probeComfy()
		}
		if comfyBase == "" {
			log.Printf("ComfyUI 未就绪(探 %v), %ds 后重试", cfg.ComfyPorts, cfg.PollSeconds)
			sleep()
			continue
		}
		n, err := runOnce()
		if err != nil {
			log.Printf("本轮失败: %v", err)
			// Comfy 端口失效则下轮重探
			if strings.Contains(err.Error(), "ComfyUI") {
				comfyBase = ""
			}
		} else if n > 0 {
			log.Printf("本轮完成 %d 个上传", n)
		}
		sleep()
	}
}

func sleep() { time.Sleep(time.Duration(cfg.PollSeconds) * time.Second) }

var lastProbe time.Time

// probeComfy 探测本机 ComfyUI 端口(常见8188/18188), 取先存活的。
func probeComfy() {
	lastProbe = time.Now()
	for _, p := range cfg.ComfyPorts {
		base := fmt.Sprintf("http://127.0.0.1:%d", p)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/queue", nil)
		resp, err := httpc.Do(req)
		cancel()
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				comfyBase = base
				log.Printf("ComfyUI 就绪: %s", base)
				return
			}
		}
	}
}

// runOnce 拉取待传清单并逐个处理, 返回成功数。
func runOnce() (int, error) {
	tasks, err := fetchPending()
	if err != nil {
		return 0, err
	}
	done := 0
	for _, t := range tasks {
		if err := handleTask(t); err != nil {
			log.Printf("任务 %s 失败: %v", t.JobID, err)
			continue
		}
		log.Printf("任务 %s -> %s 上传完成", t.JobID, t.OssKey)
		done++
	}
	return done, nil
}

func fetchPending() ([]task, error) {
	req, err := http.NewRequest(http.MethodPost, cfg.Server+"/api/v1/agent/pending", strings.NewReader("{}"))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("服务端不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, fmt.Errorf("pending HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var env struct {
		Success bool `json:"success"`
		Data    struct {
			Tasks []task `json:"tasks"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("pending 响应异常")
	}
	return env.Data.Tasks, nil
}

// handleTask 取图 → 转码 JPEG → PUT OSS → 回报完成。
func handleTask(t task) error {
	// 1) 本机 Comfy 取字节
	q := url.Values{}
	q.Set("filename", t.Filename)
	q.Set("subfolder", t.Subfolder)
	q.Set("type", "output") // 我们工作流的 SaveImage 固定 type=output
	resp, err := httpc.Get(comfyBase + "/view?" + q.Encode())
	if err != nil {
		return fmt.Errorf("ComfyUI /view: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ComfyUI /view HTTP %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// 2) 转码 JPEG(与服务端现网落盘口径一致: q90, 体积缩至 PNG 1/4~1/5)
	data := raw
	if jpg, ok := toJPEG(raw); ok {
		data = jpg
	}

	// 3) PUT OSS(V1 header 签名, 已对真实桶验证的算法)
	if err := putOSS(t.OssKey, data); err != nil {
		return fmt.Errorf("OSS 上传: %w", err)
	}

	// 4) 回报服务端
	return reportDone(t.JobID)
}

func reportDone(jobID string) error {
	body, _ := json.Marshal(map[string]string{"job_id": jobID})
	req, err := http.NewRequest(http.MethodPost, cfg.Server+"/api/v1/agent/done", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.AgentToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpc.Do(req)
	if err != nil {
		return fmt.Errorf("服务端不可达: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("done HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// putOSS V1 header 签名 PUT(虚拟主机风格; 签名资源带桶名; Content-MD5/Type 参与签名)。
func putOSS(key string, data []byte) error {
	sum := md5.Sum(data)
	md5b64 := base64.StdEncoding.EncodeToString(sum[:])
	date := time.Now().UTC().Format(http.TimeFormat)
	resource := "/" + cfg.Oss.Bucket + "/" + key
	sts := strings.Join([]string{"PUT", md5b64, "image/jpeg", date, resource}, "\n")
	mac := hmac.New(sha1.New, []byte(cfg.Oss.AkSecret))
	mac.Write([]byte(sts))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	segs := strings.Split(strings.TrimPrefix(key, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	// PUT 走 header 认证(与服务端验证过的 V1 算法一致), 非预签名 query
	putURL := fmt.Sprintf("https://%s.%s/%s", cfg.Oss.Bucket, cfg.Oss.Endpoint, strings.Join(segs, "/"))
	req, err := http.NewRequest(http.MethodPut, putURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Date", date)
	req.Header.Set("Content-Type", "image/jpeg")
	req.Header.Set("Content-MD5", md5b64)
	req.Header.Set("Authorization", "OSS "+cfg.Oss.AkID+":"+sig)
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return fmt.Errorf("PUT HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

// toJPEG 转码压缩(与服务端 queue.toJPEG 同口径 q90)。
func toJPEG(src []byte) ([]byte, bool) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, false
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}
