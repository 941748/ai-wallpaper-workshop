package comfyui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wallpaper/internal/backend"
)

// gradientPNG 生成一张渐变测试 PNG。
func gradientPNG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func TestSubmitWaitRoundTrip(t *testing.T) {
	pngBytes := gradientPNG(8, 8)
	var historyCalls atomic.Int32
	var submitted struct {
		Positive string
		Width    int
		Height   int
		Seed     float64
		Steps    float64
		CFG      float64
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Prompt map[string]struct {
				ClassType string                     `json:"class_type"`
				Inputs    map[string]json.RawMessage `json:"inputs"`
			} `json:"prompt"`
		}
		raw := new(bytes.Buffer)
		_, _ = raw.ReadFrom(r.Body)
		if strings.Contains(raw.String(), "{{") {
			t.Errorf("request still contains placeholder: %s", raw.String())
		}
		if err := json.Unmarshal(raw.Bytes(), &body); err != nil {
			t.Fatalf("bad prompt JSON: %v", err)
		}
		_ = json.Unmarshal(body.Prompt["57:27"].Inputs["text"], &submitted.Positive)
		_ = json.Unmarshal(body.Prompt["57:13"].Inputs["width"], &submitted.Width)
		_ = json.Unmarshal(body.Prompt["57:13"].Inputs["height"], &submitted.Height)
		_ = json.Unmarshal(body.Prompt["57:3"].Inputs["seed"], &submitted.Seed)
		_ = json.Unmarshal(body.Prompt["57:3"].Inputs["steps"], &submitted.Steps)
		_ = json.Unmarshal(body.Prompt["57:3"].Inputs["cfg"], &submitted.CFG)
		if got := r.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("missing bearer token, got %q", got)
		}
		_, _ = w.Write([]byte(`{"prompt_id":"job-1","number":1}`))
	})
	mux.HandleFunc("/history/job-1", func(w http.ResponseWriter, r *http.Request) {
		if historyCalls.Add(1) < 3 {
			_, _ = w.Write([]byte(`{}`)) // 前两次未完成
			return
		}
		_, _ = fmt.Fprint(w, `{"job-1":{"outputs":{"9":{"images":[{"filename":"wallpaper_00001_.png","subfolder":"","type":"output"}]}}}}`)
	})
	mux.HandleFunc("/view", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("filename") != "wallpaper_00001_.png" || q.Get("type") != "output" {
			t.Errorf("unexpected view query: %v", q)
		}
		_, _ = w.Write(pngBytes)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := New(srv.URL, "tok123")
	c.PollInterval = 5 * time.Millisecond
	c.Timeout = 3 * time.Second

	ctx := context.Background()
	id, err := c.Submit(ctx, backend.GenParams{
		Positive: `a "quoted" scene, 中文, back\slash`,
		Negative: "bad, ugly",
		Width:    1344,
		Height:   768,
		Seed:     12345,
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if id != "job-1" {
		t.Fatalf("want job-1, got %s", id)
	}
	data, err := c.Wait(ctx, id)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("result not a PNG: %v", err)
	}
	if b := img.Bounds(); b.Dx() != 8 || b.Dy() != 8 {
		t.Fatalf("unexpected image size: %v", b)
	}
	if submitted.Positive != `a "quoted" scene, 中文, back\slash` {
		t.Fatalf("positive escaping broken: %q", submitted.Positive)
	}
	if submitted.Width != 1344 || submitted.Height != 768 {
		t.Fatalf("size wrong: %dx%d", submitted.Width, submitted.Height)
	}
	if submitted.Seed != 12345 {
		t.Fatalf("seed wrong: %v", submitted.Seed)
	}
	// 工作流其余参数冻结: 步数/CFG 必须与模板一致, 不被渲染器改动
	if submitted.Steps != 8 {
		t.Fatalf("steps should stay 8, got %v", submitted.Steps)
	}
	if submitted.CFG != 1 {
		t.Fatalf("cfg should stay 1, got %v", submitted.CFG)
	}
	if historyCalls.Load() < 3 {
		t.Fatalf("expected polling, got %d calls", historyCalls.Load())
	}
}

func TestSubmitHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid workflow", http.StatusBadRequest)
	}))
	defer srv.Close()
	c := New(srv.URL, "")
	if _, err := c.Submit(context.Background(), backend.GenParams{Positive: "x", Width: 512, Height: 512}); err == nil {
		t.Fatal("expected error on HTTP 400")
	} else if !strings.Contains(err.Error(), "400") {
		t.Fatalf("error should mention status: %v", err)
	}
}

func TestWaitTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/prompt"):
			_, _ = w.Write([]byte(`{"prompt_id":"never"}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()
	c := New(srv.URL, "")
	c.PollInterval = 2 * time.Millisecond
	c.Timeout = 50 * time.Millisecond
	ctx := context.Background()
	id, err := c.Submit(ctx, backend.GenParams{Positive: "x", Width: 512, Height: 512})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Wait(ctx, id); err == nil {
		t.Fatal("expected timeout error")
	}
}
