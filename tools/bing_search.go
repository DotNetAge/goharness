package tools

import (
	"context"
	"net/url"
	"time"

	"github.com/DotNetAge/goharness/logging"
)

// BingAdapter 使用必应中国（cn.bing.com）进行搜索。
// 必应结果页的标题链接是真实直链（无需解析重定向），
// 且独立于搜狗系风控，作为搜狗/微信被风控时的兜底引擎。
type BingAdapter struct {
	client *stealthClient
}

func NewBingAdapter(logger logging.Logger) *BingAdapter {
	return &BingAdapter{
		client: newSearchStealthClient(logger, 12*time.Second),
	}
}

func (a *BingAdapter) Name() string { return "bing" }

func (a *BingAdapter) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("q", query)
	// ensearch=0 强制中文版，避免 cn.bing.com 把中文查询重定向到国际版
	params.Set("ensearch", "0")
	reqURL := "https://cn.bing.com/search?" + params.Encode()

	body, err := fetchBody(ctx, a.client, reqURL, nil)
	if err != nil {
		return nil, err
	}

	// 必应结果标题是真实直链，无需解析重定向
	return extractResultsWithGoquery(body, "li.b_algo h2 a", "href", "https://cn.bing.com", nil, opts.MaxResults), nil
}
