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

	"github.com/DotNetAge/goreact/core"
)

// --- Baidu Search Adapter ---

// BaiduAdapter implements SearchAdapter using Baidu HTML search.
// This provides a search provider optimized for Chinese-language content.
type BaiduAdapter struct {
	client *http.Client
}

// NewBaiduAdapter creates a new Baidu search adapter.
func NewBaiduAdapter() *BaiduAdapter {
	return &BaiduAdapter{
		client: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (a *BaiduAdapter) Name() string { return "baidu" }

func (a *BaiduAdapter) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("wd", query)
	params.Set("rn", strconv.Itoa(opts.MaxResults))
	params.Set("ie", "utf-8")

	reqURL := "https://www.baidu.com/s?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
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

	results := parseBaiduHTML(body)
	return filterResults(results, opts), nil
}

// parseBaiduHTML extracts search results from Baidu HTML response.
//
// Baidu search result HTML structure (subject to change):
//
//	<div class="result c-container" ...>
//	    <h3 class="t"><a href="REDIRECT_URL" ...>TITLE</a></h3>
//	    <div class="c-abstract">SNIPPET</div>
//	    <span class="c-showurl">DISPLAY_URL</span>
//	</div>
func parseBaiduHTML(html []byte) []SearchResult {
	content := string(html)
	var results []SearchResult

	// Split by result containers. Baidu uses "result c-container" as the class.
	// Try multiple patterns to be robust.
	parts := splitBaiduResults(content)
	if len(parts) <= 1 {
		return nil
	}

	for _, part := range parts[1:] {
		title, href := extractBaiduTitle(part)
		snippet := extractBaiduSnippet(part)
		displayURL := extractBaiduShowURL(part)

		if title == "" {
			continue
		}

		finalURL := resolveBaiduURL(href, displayURL)
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

// splitBaiduResults splits HTML into result blocks.
func splitBaiduResults(content string) []string {
	// Try primary pattern: class="result c-container"
	parts := strings.Split(content, `class="result`)
	if len(parts) > 1 {
		return parts
	}
	// Try fallback: class="c-container"
	return strings.Split(content, `class="c-container"`)
}

// extractBaiduTitle extracts the title text and href from a result block.
func extractBaiduTitle(part string) (title, href string) {
	// Find the h3 with class "t"
	h3Start := strings.Index(part, `<h3`)
	if h3Start < 0 {
		return "", ""
	}
	h3Part := part[h3Start:]
	h3End := strings.Index(h3Part, `</h3>`)
	if h3End < 0 {
		h3End = len(h3Part)
	}

	// Extract href from <a> inside h3
	aPart := h3Part[:h3End]
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

	// Extract text content between > and </a>
	if gt := strings.Index(aPart, ">"); gt >= 0 {
		textPart := aPart[gt+1:]
		// Remove nested tags like <em>, <span> etc. but keep the text
		if end := strings.Index(textPart, "</a>"); end >= 0 {
			title = strings.TrimSpace(textPart[:end])
		} else {
			title = strings.TrimSpace(textPart)
		}
	}

	return title, href
}

// extractBaiduSnippet extracts the snippet from a result block.
func extractBaiduSnippet(part string) string {
	// Try multiple possible snippet class names
	for _, class := range []string{
		`class="c-abstract"`,
		`class="c-span-last"`,
		`class="content-right_`,
	} {
		if idx := strings.Index(part, class); idx >= 0 {
			snippetPart := part[idx+len(class):]
			if gt := strings.Index(snippetPart, ">"); gt >= 0 {
				snippetPart = snippetPart[gt+1:]
				endTags := []string{"</div>", "</span>"}
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

// extractBaiduShowURL extracts the display URL from a result block.
func extractBaiduShowURL(part string) string {
	for _, class := range []string{
		`class="c-showurl"`,
		`class="c-color-gray"`,
	} {
		if idx := strings.Index(part, class); idx >= 0 {
			urlPart := part[idx+len(class):]
			if gt := strings.Index(urlPart, ">"); gt >= 0 {
				urlPart = urlPart[gt+1:]
				if end := strings.Index(urlPart, "</span>"); end >= 0 {
					return strings.TrimSpace(urlPart[:end])
				}
				if end := strings.Index(urlPart, "<"); end >= 0 {
					return strings.TrimSpace(urlPart[:end])
				}
			}
		}
	}
	return ""
}

// resolveBaiduURL resolves the final URL from href and display URL.
func resolveBaiduURL(href, displayURL string) string {
	// If href is already a valid URL (not a raw token), use it
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		if realURL := decodeBaiduRedirectURL(href); realURL != "" {
			return realURL
		}
		return href
	}

	// href might be a raw Baidu redirect token (e.g., "wtYPKHW...")
	// Try to construct and decode a Baidu redirect URL from it
	if len(href) > 10 && !strings.Contains(href, " ") {
		constructed := "http://www.baidu.com/link?url=" + href
		if realURL := decodeBaiduRedirectURL(constructed); realURL != "" {
			return realURL
		}
	}

	// Use display URL as fallback
	if displayURL != "" {
		if !strings.HasPrefix(displayURL, "http") {
			return "https://" + displayURL
		}
		return displayURL
	}

	return ""
}

// decodeBaiduRedirectURL extracts the real URL from a Baidu redirect link.
// Baidu links look like: http://www.baidu.com/link?url=ENCODED_REAL_URL
func decodeBaiduRedirectURL(href string) string {
	if !strings.Contains(href, "baidu.com/link") {
		return ""
	}
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
	// Validate the decoded result looks like a real URL
	if strings.HasPrefix(decoded, "http://") || strings.HasPrefix(decoded, "https://") {
		return decoded
	}
	return ""
}

// --- BaiduSearchTool ---

// BaiduSearchTool performs web searches via Baidu.
// It is a standalone tool (not using the adapter chain) for direct Baidu access.
type BaiduSearchTool struct {
	adapter *BaiduAdapter
}

// NewBaiduSearchTool creates a Baidu search tool.
func NewBaiduSearchTool() core.FuncTool {
	return &BaiduSearchTool{
		adapter: NewBaiduAdapter(),
	}
}

func (t *BaiduSearchTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name: "BaiduSearch",
		Description: `Search the web using Baidu, optimized for Chinese-language content.
Returns a list of {title, url, snippet} results. Use WebFetch to read full page content.`,
		Prompt: `Search the web using Baidu for real-time information, especially Chinese-language content.
Returns search result information formatted as search result blocks, including links as markdown hyperlinks.
Use this tool for accessing Chinese web resources and up-to-date information beyond the model's knowledge cutoff.

CRITICAL REQUIREMENT - You MUST follow this:
- After answering the user's question, you MUST include a "Sources:" section at the end of your response
- In the Sources section, list all relevant URLs from the search results as markdown hyperlinks: [Title](URL)
- This is MANDATORY - never skip including sources in your response

Usage notes:
- Domain filtering is supported to include or block specific websites
- This tool is optimized for Chinese-language search queries`,
		IsReadOnly: true,
		Tags:       []string{"web", "search", "baidu", "chinese"},
		Parameters: []core.Parameter{
			{
				Name:        "query",
				Type:        "string",
				Description: "The search query string. Chinese queries work best.",
				Required:    true,
			},
			{
				Name:        "max_results",
				Type:        "integer",
				Description: "Maximum number of results to return (default: 10, max: 20).",
				Required:    false,
			},
			{
				Name:        "allowed_domains",
				Type:        "array",
				Description: "Restrict results to these domains.",
				Required:    false,
			},
			{
				Name:        "blocked_domains",
				Type:        "array",
				Description: "Exclude results from these domains.",
				Required:    false,
			},
		},
	}
}

func (t *BaiduSearchTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	query, err := ValidateRequiredString(params, "query")
	if err != nil {
		return nil, err
	}
	if len(query) < 2 {
		return nil, fmt.Errorf("query must be at least 2 characters")
	}

	maxResults := 10
	if raw, ok := params["max_results"]; ok {
		if v, ok := ToFloat64(raw); ok && v > 0 {
			maxResults = int(v)
			if maxResults > 20 {
				maxResults = 20
			}
		}
	}

	var allowedDomains, blockedDomains []string
	if raw, ok := params["allowed_domains"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				allowedDomains = append(allowedDomains, s)
			}
		}
	}
	if raw, ok := params["blocked_domains"].([]any); ok {
		for _, v := range raw {
			if s, ok := v.(string); ok {
				blockedDomains = append(blockedDomains, s)
			}
		}
	}

	opts := SearchOptions{
		MaxResults:     maxResults,
		AllowedDomains: allowedDomains,
		BlockedDomains: blockedDomains,
	}

	results, err := t.adapter.Search(ctx, query, opts)
	if err != nil {
		return nil, fmt.Errorf("baidu search failed: %w", err)
	}

	if len(results) == 0 {
		return fmt.Sprintf("No results found for query: %q\n\nPossible reasons:\n- Query too specific or contains typos\n- Baidu may be rate-limiting or temporarily unavailable\n- Network connectivity problems\n\nSuggestion: Try simplifying the query or search again later.", query), nil
	}

	return formatSearchResults(query, results), nil
}
