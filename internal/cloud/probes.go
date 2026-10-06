package cloud

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

// ProbeItem 公共探针池中的一张探针图。
type ProbeItem struct {
	ID     string            `json:"id"`     // p01..p12(与客户端平衡组合算法一致)
	Values map[string]string `json:"values"` // dim → valueID
	Seed   int64             `json:"seed"`
	URL    string            `json:"url"` // 相对路径, 例 /api/v1/probes/image?id=p01
}

// ProbesSample 从云端公共探针池按平衡抽取 count 张(秒开体验)。
func (c *Client) ProbesSample(ctx context.Context, count int) ([]ProbeItem, error) {
	var out struct {
		Items []ProbeItem `json:"items"`
	}
	path := fmt.Sprintf("/probes/sample?count=%d", count)
	if err := c.doJSON(ctx, c.meta, http.MethodGet, path, nil, &out); err != nil {
		return nil, err
	}
	return out.Items, nil
}

// ProbesImage 下载一张公共探针图。
func (c *Client) ProbesImage(ctx context.Context, relURL string) ([]byte, error) {
	// 统一收敛到 /api/v1 前缀, 防越权路径
	u, err := url.Parse(relURL)
	if err != nil || u.Path == "" {
		return nil, fmt.Errorf("非法探针图地址: %s", relURL)
	}
	p := u.Path
	if len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	const prefix = "api/v1/"
	if len(p) > len(prefix) && p[:len(prefix)] == prefix {
		p = p[len(prefix):]
	}
	return c.doRaw(ctx, c.long, http.MethodGet, p)
}
