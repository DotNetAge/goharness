package tools

import (
	"context"
	"net/url"
	"time"

	"github.com/DotNetAge/goharness/logging"
)

// So360Adapter 使用 360 搜索（www.so.com）进行搜索。
// 360 结果页的标题链接是 /link?m= 重定向，但 a 标签带 data-mdurl 属性
// 指向真实 URL。本适配器用 CSS 选择器 li.res-list a[data-mdurl] 精准定位
// 主结果块，并直接取 data-mdurl 作为真实直链，绕开重定向解析。
// 360 独立于搜狗系风控，作为兜底引擎。
type So360Adapter struct {
	client *stealthClient
}

func NewSo360Adapter(logger logging.Logger) *So360Adapter {
	return &So360Adapter{
		client: newSearchStealthClient(logger, 12*time.Second),
	}
}

func (a *So360Adapter) Name() string { return "360" }

func (a *So360Adapter) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("q", query)
	reqURL := "https://www.so.com/s?" + params.Encode()

	body, err := fetchBody(ctx, a.client, reqURL, nil)
	if err != nil {
		return nil, err
	}

	// 360 主结果在 li.res-list 内，a 标签的 data-mdurl 属性即真实 URL；
	// extractResultsWithGoquery 会按 data-mdurl 去重（同一结果有标题/图片/摘要
	// 多个 a 共享同一 URL，去重后只保留第一个有标题的）
	return extractResultsWithGoquery(body, "li.res-list a[data-mdurl]", "data-mdurl", "https://www.so.com", nil, opts.MaxResults), nil
}
