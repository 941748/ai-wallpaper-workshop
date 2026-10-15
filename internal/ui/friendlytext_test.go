package ui

import (
	"errors"
	"strings"
	"testing"
)

// 回归: 后台 URL/IP/路径等技术细节严禁出现在面向用户的错误文案中(2026-10-15 用户反馈)。
func TestFriendlyTextHidesTechnicalDetails(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			`Get "https://www.cybermesh.space/api/v1/client/latest?version=1.1.3": dial tcp 47.237.98.171:443: connect: A socket operation was attempted to an unreachable host`,
			"服务暂时不可达",
		},
		{
			`出图失败: Get "http://127.0.0.1:18188/prompt": dial tcp 127.0.0.1:18188: connect: connection refused`,
			"出图失败: 服务暂时不可达",
		},
		{
			"云端 POST /api/v1/prompts/next HTTP 500: internal error",
			"暂时无法连接",
		},
		{
			`打开数据目录失败: open C:\Users\demo\AppData\Local\AIWallpaper\x: Access is denied`,
			"暂时无法连接",
		},
		// 已是友好文案的错误原样保留
		{"暂无可用图源(云端不可达且备用池空)", "暂无可用图源(云端不可达且备用池空)"},
		{"上一轮换图仍在进行中, 请稍后再试", "上一轮换图仍在进行中, 请稍后再试"},
	}
	for _, c := range cases {
		got := friendlyText(c.in)
		if got != c.want {
			t.Fatalf("friendlyText(%q) = %q, want %q", c.in, got, c.want)
		}
		for _, leak := range []string{"://", "47.237", "127.0.0.1", "cybermesh", "C:\\"} {
			if strings.Contains(got, leak) {
				t.Fatalf("friendlyText(%q) = %q 泄露技术细节 %q", c.in, got, leak)
			}
		}
	}
}

func TestFriendlyErrTextNil(t *testing.T) {
	if got := friendlyErrText(nil); got != "未知错误" {
		t.Fatalf("friendlyErrText(nil) = %q", got)
	}
	if got := friendlyErrText(errors.New("云端未就绪")); got != "云端未就绪" {
		t.Fatalf("friendlyErrText friendly msg = %q", got)
	}
}
