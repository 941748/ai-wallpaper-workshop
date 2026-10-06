package cloud

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"wallpaper/internal/signals"
)

// mockCloudServer 统计 /prompts 与 /signals 的请求数; fail 时返回 500。
func mockCloudServer(t *testing.T, fail *atomic.Bool) (*httptest.Server, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var prompts, sigs atomic.Int32
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter) {
		if fail != nil && fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"success":false,"error":"boom"}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	}
	mux.HandleFunc("/api/v1/prompts", func(w http.ResponseWriter, r *http.Request) {
		prompts.Add(1)
		ok(w)
	})
	mux.HandleFunc("/api/v1/signals", func(w http.ResponseWriter, r *http.Request) {
		sigs.Add(1)
		ok(w)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &prompts, &sigs
}

func samplePromptRecord() PromptRecord {
	return PromptRecord{
		At: time.Now(), Positive: "mountain lake", Width: 1344, Height: 768,
		Source: "local", Success: true, ProfileVer: 1,
	}
}

// 补传成功: 队列清空, 云端收到 prompt 与 signals 各一次。
func TestQueueFlushDeliversAndClears(t *testing.T) {
	srv, prompts, sigs := mockCloudServer(t, nil)
	dir := t.TempDir()
	c := New(srv.URL, "u1", "tok", "dev")

	if err := Enqueue(dir, "prompt", samplePromptRecord()); err != nil {
		t.Fatal(err)
	}
	evs := []signals.Event{{ID: "ev1", At: time.Now(), Type: signals.TypeStyleReject, Detail: "换个风格"}}
	if err := Enqueue(dir, "signals", evs); err != nil {
		t.Fatal(err)
	}

	c.FlushQueue(context.Background(), dir)
	if prompts.Load() != 1 || sigs.Load() != 1 {
		t.Fatalf("云端收到 prompts=%d signals=%d, 期望各 1", prompts.Load(), sigs.Load())
	}
	items, err := LoadQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("补传成功后队列应为空, 实际 %d 条", len(items))
	}
}

// 网络失败: 条目保留待下轮重试。
func TestQueueKeptOnNetworkFailure(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	srv, _, _ := mockCloudServer(t, &fail)
	dir := t.TempDir()
	c := New(srv.URL, "u1", "tok", "dev")

	if err := Enqueue(dir, "prompt", samplePromptRecord()); err != nil {
		t.Fatal(err)
	}
	c.FlushQueue(context.Background(), dir)
	items, err := LoadQueue(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("网络失败时条目应保留, 实际 %d 条", len(items))
	}

	// 恢复后补传成功
	fail.Store(false)
	c.FlushQueue(context.Background(), dir)
	items, _ = LoadQueue(dir)
	if len(items) != 0 {
		t.Fatalf("恢复后应补传成功清空, 实际 %d 条", len(items))
	}
}

// 超过 7 天: 直接丢弃, 不再上报。
func TestQueueDropsExpiredEntries(t *testing.T) {
	srv, prompts, _ := mockCloudServer(t, nil)
	dir := t.TempDir()
	c := New(srv.URL, "u1", "tok", "dev")

	payload, _ := json.Marshal(samplePromptRecord())
	old := []QueuedItem{{At: time.Now().Add(-8 * 24 * time.Hour), Kind: "prompt", Payload: payload}}
	if err := saveQueue(dir, old); err != nil {
		t.Fatal(err)
	}

	c.FlushQueue(context.Background(), dir)
	if prompts.Load() != 0 {
		t.Fatalf("过期条目不应上报, 云端收到 %d 次", prompts.Load())
	}
	items, _ := LoadQueue(dir)
	if len(items) != 0 {
		t.Fatalf("过期条目应被丢弃, 实际 %d 条", len(items))
	}
}

// 坏负载: 跳过该条, 不影响后续正常条目。
func TestQueueSkipsMalformedPayload(t *testing.T) {
	srv, prompts, _ := mockCloudServer(t, nil)
	dir := t.TempDir()
	c := New(srv.URL, "u1", "tok", "dev")

	good, _ := json.Marshal(samplePromptRecord())
	items := []QueuedItem{
		{At: time.Now(), Kind: "prompt", Payload: json.RawMessage(`"not-an-object"`)},
		{At: time.Now(), Kind: "prompt", Payload: good},
	}
	if err := saveQueue(dir, items); err != nil {
		t.Fatal(err)
	}

	c.FlushQueue(context.Background(), dir)
	if prompts.Load() != 1 {
		t.Fatalf("坏负载应跳过、正常条目仍上报, 云端收到 %d 次", prompts.Load())
	}
	rest, _ := LoadQueue(dir)
	if len(rest) != 0 {
		t.Fatalf("队列应清空, 实际 %d 条", len(rest))
	}
}
