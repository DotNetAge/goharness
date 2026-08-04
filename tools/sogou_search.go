package tools

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/logging"
)

type SogouAdapter struct {
	client *stealthClient
}

func NewSogouAdapter(logger logging.Logger) *SogouAdapter {
	return &SogouAdapter{
		client: newSearchStealthClient(logger, 15*time.Second),
	}
}

func (a *SogouAdapter) Name() string { return "sogou" }

func (a *SogouAdapter) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("page", "1")
	num := opts.MaxResults
	if num > 10 {
		num = 10
	}
	params.Set("num", strconv.Itoa(num))

	reqURL := "https://www.sogou.com/web?" + params.Encode()
	body, err := fetchBody(ctx, a.client, reqURL, nil)
	if err != nil {
		return nil, err
	}

	results := extractResultsWithGoquery(body, "div.vrwrap h3.vr-title a, h3.vr-title a", "href", "https://www.sogou.com", func(h string) string {
		return resolveSogouURL(ctx, a.client, h, reqURL)
	}, opts.MaxResults)

	// 过滤掉未解析的 sogou.com 重定向链接
	filtered := make([]SearchResult, 0, len(results))
	for _, r := range results {
		if strings.HasPrefix(r.URL, "http") && !strings.Contains(r.URL, "sogou.com") {
			filtered = append(filtered, r)
		}
	}
	return filtered, nil
}

func resolveSogouURL(ctx context.Context, client *stealthClient, href, searchURL string) string {
	if href == "" {
		return ""
	}

	// 1) 尝试从 /link?url=... 中提取 URL 参数
	encodedURL := extractURLParam(href)
	if strings.HasPrefix(encodedURL, "http://") || strings.HasPrefix(encodedURL, "https://") {
		return encodedURL
	}

	// 2) 已是绝对 URL
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}

	// 3) 相对搜狗重定向 — 需要 JS 重定向解析
	if strings.HasPrefix(href, "/link?") {
		fullURL := "https://www.sogou.com" + href
		if target := resolveSogouJSRedirect(ctx, client, fullURL, searchURL); target != "" {
			return target
		}
		// 回退到绝对 URL（若 WebFetch 跟随 JS 重定向则可能生效）
		return fullURL
	}

	return ""
}

// resolveSogouJSRedirect 通过隐蔽客户端获取搜狗重定向页，提取 window.location.replace 中的目标 URL。
// 不跟随重定向（FollowRedirects:false），因为搜狗返回 200 + JS 重定向而非 HTTP 302。
// 修复原实现用裸 client + 硬编码截断 UA 的指纹不一致 bug——现统一走 stealthClient，
// 与主搜索请求共享 TLS 指纹、cookiejar 与浏览器指纹头。
func resolveSogouJSRedirect(ctx context.Context, client *stealthClient, redirectURL, referer string) string {
	resp, err := client.Do(ctx, StealthRequest{
		Method:          "GET",
		URL:             redirectURL,
		Headers:         map[string][]string{"Referer": {referer}},
		FollowRedirects: false,
	})
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	// 搜狗返回 200 + JS 重定向：window.location.replace("https://real-url")
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	content := string(body[:n])

	const prefix = `window.location.replace("`
	start := strings.Index(content, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(content[start:], `"`)
	if end < 0 {
		return ""
	}
	target := content[start : start+end]
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		return target
	}
	return ""
}

func extractURLParam(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	encodedURL := u.Query().Get("url")
	if encodedURL == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(encodedURL)
	if err != nil {
		return ""
	}
	return decoded
}
