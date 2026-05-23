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

// --- 360 (Haosou) Search Adapter ---

type HaosouAdapter struct {
	client *http.Client
}

func NewHaosouAdapter() *HaosouAdapter {
	return &HaosouAdapter{
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (a *HaosouAdapter) Name() string { return "haosou" }

func (a *HaosouAdapter) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("q", query)
	params.Set("pn", "0")
	params.Set("ps", strconv.Itoa(opts.MaxResults))

	reqURL := "https://www.so.com/s?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1")
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

	results := parseHaosouHTML(body)
	return filterResults(results, opts), nil
}

func parseHaosouHTML(raw []byte) []SearchResult {
	content := string(raw)
	var results []SearchResult

	blocks := splitHaosouBlocks(content)
	for _, block := range blocks {
		if strings.Contains(block, "mso-recommend-card") || strings.Contains(block, "mohe-m-") {
			continue
		}
		title, href := extractHaosouTitle(block)
		snippet := extractHaosouSnippet(block)

		if title == "" || href == "" {
			continue
		}

		finalURL := resolveHaosouURL(href)
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

func splitHaosouBlocks(content string) []string {
	var blocks []string
	var idx int
	for {
		pos := strings.Index(content[idx:], `res-list`)
		if pos < 0 {
			break
		}
		pos += idx
		start := strings.LastIndex(content[:pos], `<div`)
		if start < 0 {
			idx = pos + 1
			continue
		}
		endTag := "</div>"
		end := strings.Index(content[pos:], endTag)
		if end < 0 {
			break
		}
		end = pos + len(endTag) + 2
		for depth := 1; depth > 0 && end < len(content); {
			nextOpen := strings.Index(content[end:], "<div")
			nextClose := strings.Index(content[end:], "</div>")
			if nextClose < 0 {
				break
			}
			if nextOpen >= 0 && nextOpen < nextClose {
				depth++
				end = end + nextOpen + 4
			} else {
				depth--
				end = end + nextClose + 6
			}
		}
		blocks = append(blocks, content[start:end])
		idx = end
	}
	return blocks
}

func extractHaosouTitle(part string) (title, href string) {
	if idx := strings.Index(part, `href="`); idx >= 0 {
		hrefPart := part[idx+6:]
		if end := strings.Index(hrefPart, `"`); end >= 0 {
			href = htmlUnescape(hrefPart[:end])
		}
	}

	h3Start := strings.Index(part, `<h3`)
	if h3Start < 0 {
		return "", href
	}
	h3Part := part[h3Start:]
	h3End := strings.Index(h3Part, `</h3>`)
	if h3End < 0 {
		h3End = len(h3Part)
	}

	if gt := strings.Index(h3Part, ">"); gt >= 0 {
		textPart := h3Part[gt+1 : h3End]
		if end := strings.Index(textPart, "</a>"); end >= 0 {
			title = strings.TrimSpace(textPart[:end])
		} else {
			title = strings.TrimSpace(textPart)
		}
	}

	return title, href
}

func extractHaosouSnippet(part string) string {
	candidates := []string{
		`class="g-main summary"`,
		`class="res-desc"`,
		`class="res-desc-info"`,
		`class="res-con-flex"`,
	}
	for _, class := range candidates {
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

func resolveHaosouURL(href string) string {
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		if realURL := decodeHaosouRedirectURL(href); realURL != "" {
			return realURL
		}
		return href
	}
	return ""
}

func decodeHaosouRedirectURL(href string) string {
	if u, err := url.Parse(href); err == nil {
		if encoded := u.Query().Get("u"); encoded != "" {
			if decoded, err := url.QueryUnescape(encoded); err == nil {
				if strings.HasPrefix(decoded, "http://") || strings.HasPrefix(decoded, "https://") {
					return decoded
				}
			}
		}
		if encoded := u.Query().Get("url"); encoded != "" {
			if decoded, err := url.QueryUnescape(encoded); err == nil {
				if strings.HasPrefix(decoded, "http://") || strings.HasPrefix(decoded, "https://") {
					return decoded
				}
			}
		}
	}
	return ""
}
