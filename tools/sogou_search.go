package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// --- Sogou (GouGou) Search Adapter ---

// SogouAdapter implements SearchAdapter using Sogou HTML search.
// 搜狗搜索引擎，提供中文内容优化。
type SogouAdapter struct {
	client *http.Client
}

// NewSogouAdapter creates a new Sogou search adapter.
func NewSogouAdapter() *SogouAdapter {
	return &SogouAdapter{
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
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
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	results := parseSogouHTML(body)
	return filterResults(results, opts), nil
}

// parseSogouHTML extracts search results from Sogou HTML response.
//
// 搜索结果HTML结构：
//
//	<div class="vrwrap">
//	    <h3 class="vr-title"><a href="URL" ...>TITLE</a></h3>
//	    <p class="str-text-info">SNIPPET</p>
//	    <span class="str-info">DISPLAY_URL</span>
//	</div>
func parseSogouHTML(html []byte) []SearchResult {
	content := string(html)
	var results []SearchResult

	parts := strings.Split(content, `class="vrResult"`)
	if len(parts) <= 1 {
		parts = strings.Split(content, `class="vrwrap"`)
	}
	if len(parts) <= 1 {
		parts = strings.Split(content, `class="rb"`)
	}
	if len(parts) <= 1 {
		return nil
	}

	for _, part := range parts[1:] {
		title, href := extractSogouTitle(part)
		snippet := extractSogouSnippet(part)

		if title == "" || href == "" {
			continue
		}

		finalURL := resolveSogouURL(href)
		if finalURL == "" {
			continue
		}

		results = append(results, SearchResult{
			Title:   normalizeHTML(title),
			URL:     finalURL,
			Snippet: normalizeHTML(snippet),
		})
	}

	return results
}

// extractSogouTitle extracts the title text and href from a result block.
func extractSogouTitle(part string) (title, href string) {
	for _, tag := range []string{"h3", "h4"} {
		tagStart := strings.Index(part, "<"+tag)
		if tagStart < 0 {
			continue
		}
		tagPart := part[tagStart:]
		tagEnd := strings.Index(tagPart, "</"+tag+">")
		if tagEnd < 0 {
			tagEnd = len(tagPart)
		}

		aPart := tagPart[:tagEnd]

		// Extract href from <a> tag
		if idx := strings.Index(aPart, `href="`); idx >= 0 {
			hrefPart := aPart[idx+6:]
			if end := strings.Index(hrefPart, `"`); end >= 0 {
				href = htmlUnescape(hrefPart[:end])
			}
		}
		if idx := strings.Index(aPart, `href='`); idx >= 0 && href == "" {
			hrefPart := aPart[idx+6:]
			if end := strings.Index(hrefPart, `'`); end >= 0 {
				href = htmlUnescape(hrefPart[:end])
			}
		}

		// Extract title text inside <a> tag (not the full element)
		aTagStart := strings.Index(aPart, "<a")
		if aTagStart >= 0 {
			afterA := aPart[aTagStart:]
			// Find the > that closes the <a> opening tag
			aClose := strings.Index(afterA, ">")
			if aClose >= 0 {
				innerText := afterA[aClose+1:]
				if end := strings.Index(innerText, "</a>"); end >= 0 {
					title = strings.TrimSpace(innerText[:end])
				}
			}
		}

		if title != "" || href != "" {
			break
		}
	}

	return title, href
}

// extractSogouSnippet extracts the snippet from a result block.
func extractSogouSnippet(part string) string {
	for _, class := range []string{
		`class="str-text-info"`,
		`class="str_info"`,
		`class="fb"`,
	} {
		if idx := strings.Index(part, class); idx >= 0 {
			snippetPart := part[idx+len(class):]
			if gt := strings.Index(snippetPart, ">"); gt >= 0 {
				snippetPart = snippetPart[gt+1:]
				endTags := []string{"</p>", "</div>", "</span>"}
				for _, endTag := range endTags {
					if end := strings.Index(snippetPart, endTag); end >= 0 {
						return strings.TrimSpace(snippetPart[:end])
					}
				}
				return strings.TrimSpace(snippetPart)
			}
		}
	}
	return ""
}

// resolveSogouURL resolves the final URL from Sogou search result href.
// Handles both:
//   - Direct URLs: https://example.com
//   - Sogou redirect URLs: https://www.sogou.com/link?url=...
//   - Mobile relative redirect URLs: ./id=.../tc?url=...
func resolveSogouURL(href string) string {
	if href == "" {
		return ""
	}

	// Try to extract url parameter from any redirect format (supports ./id=.../tc?url=... as well)
	if encodedURL := extractURLParam(href); encodedURL != "" {
		return encodedURL
	}

	// Direct URL
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}

	return ""
}

// extractURLParam attempts to extract a url query parameter from any href.
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
	if strings.HasPrefix(decoded, "http://") || strings.HasPrefix(decoded, "https://") {
		return decoded
	}
	return ""
}
