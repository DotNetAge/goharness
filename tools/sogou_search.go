package tools

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type SogouAdapter struct {
	client *http.Client
}

func NewSogouAdapter() *SogouAdapter {
	return &SogouAdapter{
		client: &http.Client{Timeout: 15 * time.Second},
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
	md, err := fetchAndExtract(ctx, a.client, reqURL, nil)
	if err != nil {
		return nil, err
	}

	results := extractSearchResultsFromMarkdown(md, opts.MaxResults)
	for i := range results {
		if resolved := resolveSogouURL(results[i].URL, reqURL); resolved != "" {
			results[i].URL = resolved
		}
	}

	// Drop results whose URLs are still unresolved Sogou redirects
	filtered := make([]SearchResult, 0, len(results))
	for _, r := range results {
		if strings.HasPrefix(r.URL, "http") && !strings.Contains(r.URL, "sogou.com") {
			filtered = append(filtered, r)
		}
	}
	return filterResults(filtered, opts), nil
}

func resolveSogouURL(href string, searchURL string) string {
	if href == "" {
		return ""
	}

	// 1) Try to extract URL parameter from /link?url=...
	encodedURL := extractURLParam(href)
	if strings.HasPrefix(encodedURL, "http://") || strings.HasPrefix(encodedURL, "https://") {
		return encodedURL
	}

	// 2) Already absolute
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}

	// 3) Relative Sogou redirect — need JS redirect resolution
	if strings.HasPrefix(href, "/link?") {
		fullURL := "https://www.sogou.com" + href
		if target := resolveSogouJSRedirect(fullURL, searchURL); target != "" {
			return target
		}
		// Fall back to absolute URL (may work if WebFetch follows JS redirect)
		return fullURL
	}

	return ""
}

// resolveSogouJSRedirect fetches a Sogou redirect page and extracts the
// target URL from window.location.replace("...").
func resolveSogouJSRedirect(redirectURL, referer string) string {
	client := &http.Client{
		Timeout: 8 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // don't follow redirects
		},
	}
	req, err := http.NewRequest("GET", redirectURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Referer", referer)
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")

	resp, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()

	// Sogou returns 200 with JS redirect: window.location.replace("https://real-url")
	body := make([]byte, 4096)
	n, _ := resp.Body.Read(body)
	content := string(body[:n])

	// Extract target URL from JS redirect
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
