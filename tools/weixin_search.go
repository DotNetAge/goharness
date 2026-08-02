package tools

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/logging"
)

type WeixinAdapter struct {
	client *stealthClient
}

func NewWeixinAdapter(logger logging.Logger) *WeixinAdapter {
	return &WeixinAdapter{
		client: newSearchStealthClient(logger, 10*time.Second),
	}
}

func (a *WeixinAdapter) Name() string { return "weixin" }

func (a *WeixinAdapter) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("type", "2")
	params.Set("ie", "utf8")

	reqURL := "https://weixin.sogou.com/weixin?" + params.Encode()
	body, err := fetchBody(ctx, a.client, reqURL, map[string]string{"Referer": "https://weixin.sogou.com/"})
	if err != nil {
		return nil, err
	}

	results := extractResultsWithGoquery(body, "div.vrwrap h3 a, div.txt-box h3 a", "href", "https://weixin.sogou.com", resolveWeixinURL, opts.MaxResults)

	// 过滤掉未解析的 sogou.com 重定向链接
	filtered := make([]SearchResult, 0, len(results))
	for _, r := range results {
		if strings.HasPrefix(r.URL, "http") && !strings.Contains(r.URL, "sogou.com") {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}

func resolveWeixinURL(href string) string {
	if strings.HasPrefix(href, "/link?") {
		u, err := url.Parse("https://weixin.sogou.com" + href)
		if err != nil {
			return href
		}
		encodedURL := u.Query().Get("url")
		if encodedURL != "" {
			decoded, err := url.QueryUnescape(encodedURL)
			if err == nil && (strings.HasPrefix(decoded, "http://") || strings.HasPrefix(decoded, "https://")) {
				return decoded
			}
		}
		return u.String()
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	return "https://weixin.sogou.com" + href
}
