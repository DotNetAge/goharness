package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/chromedp/chromedp"
)

// --- Chrome-based Search Adapter ---

// ChromeSearchAdapter provides a base for search engines that require
// JavaScript rendering or have anti-scraping protections.
// 使用无头Chrome浏览器执行搜索，可以绕过反爬虫机制。
type ChromeSearchAdapter struct {
	name        string
	searchURL   string
	resultFunc  func(string) []SearchResult
	timeout     time.Duration
}

// NewChromeSearchAdapter creates a new Chrome-based search adapter.
//
// Parameters:
//   - name: adapter identifier (e.g., "haosou", "sogou")
//   - searchURL: URL template with %s placeholder for query
//   - resultFunc: function to parse HTML and extract results
//   - timeout: request timeout (default 30s if 0)
func NewChromeSearchAdapter(name, searchURL string, resultFunc func(string) []SearchResult, timeout time.Duration) *ChromeSearchAdapter {
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	return &ChromeSearchAdapter{
		name:        name,
		searchURL:   searchURL,
		resultFunc:  resultFunc,
		timeout:     timeout,
	}
}

func (a *ChromeSearchAdapter) Name() string { return a.name }

func (a *ChromeSearchAdapter) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	// Create chromedp allocator context from the parent context.
	// This ensures chromedp has a valid allocator even if the caller
	// passes a plain context.Background().
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx,
		append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.Flag("headless", true),
			chromedp.Flag("disable-gpu", true),
			chromedp.Flag("no-sandbox", true),
		)...,
	)
	defer allocCancel()

	chromeCtx, chromeCancel := chromedp.NewContext(allocCtx)
	defer chromeCancel()

	var htmlContent string

	err := chromedp.Run(chromeCtx,
		chromedp.Navigate(fmt.Sprintf(a.searchURL, query)),
		chromedp.WaitVisible(`body`, chromedp.ByQuery),
		chromedp.Sleep(2*time.Second),
		chromedp.OuterHTML(`html`, &htmlContent, chromedp.ByQuery),
	)
	if err != nil {
		return nil, fmt.Errorf("chrome search failed: %w", err)
	}

	results := a.resultFunc(htmlContent)
	return filterResults(results, opts), nil
}

// --- Haosou Chrome Adapter ---

// HaosouChromeAdapter implements SearchAdapter using headless Chrome for Haosou.
// 使用Chrome绕过360搜索的反爬虫机制。
type HaosouChromeAdapter struct {
	*ChromeSearchAdapter
}

// NewHaosouChromeAdapter creates a new Haosou Chrome search adapter.
func NewHaosouChromeAdapter() *HaosouChromeAdapter {
	return &HaosouChromeAdapter{
		ChromeSearchAdapter: NewChromeSearchAdapter(
			"haosou",
			"https://www.so.com/s?q=%s",
			func(html string) []SearchResult { return parseHaosouHTML([]byte(html)) },
			30*time.Second,
		),
	}
}

// --- Sogou Chrome Adapter ---

// SogouChromeAdapter implements SearchAdapter using headless Chrome for Sogou.
// 使用Chrome确保搜狗移动端页面的JavaScript正确渲染。
type SogouChromeAdapter struct {
	*ChromeSearchAdapter
}

// NewSogouChromeAdapter creates a new Sogou Chrome search adapter.
func NewSogouChromeAdapter() *SogouChromeAdapter {
	return &SogouChromeAdapter{
		ChromeSearchAdapter: NewChromeSearchAdapter(
			"sogou",
			"https://m.sogou.com/web/searchList.jsp?keyword=%s",
			func(html string) []SearchResult { return parseSogouHTML([]byte(html)) },
			30*time.Second,
		),
	}
}