package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// --- Baidu Search Adapter ---

var baiduCookies = []string{
	"BAIDUID=0B12E3F4A5B6C7D8:FG=1; BIDUPSID=0B12E3F4A5B6C7D8;",
	"BAIDUID=1C23D4E5F6A7B8C9:FG=1; BIDUPSID=1C23D4E5F6A7B8C9;",
	"BAIDUID=2D34E5F6A7B8C9D0:FG=1; BIDUPSID=2D34E5F6A7B8C9D0;",
	"BAIDUID=3E45F6A7B8C9D0E1:FG=1; BIDUPSID=3E45F6A7B8C9D0E1;",
	"BAIDUID=4F56A7B8C9D0E1F2:FG=1; BIDUPSID=4F56A7B8C9D0E1F2;",
	"BAIDUID=5A67B8C9D0E1F2A3:FG=1; BIDUPSID=5A67B8C9D0E1F2A3;",
	"BAIDUID=6B78C9D0E1F2A3B4:FG=1; BIDUPSID=6B78C9D0E1F2A3B4;",
	"BAIDUID=7C89D0E1F2A3B4C5:FG=1; BIDUPSID=7C89D0E1F2A3B4C5;",
}

var baiduCookieMu sync.Mutex
var baiduCookieIdx int

func nextBaiduCookie() string {
	baiduCookieMu.Lock()
	defer baiduCookieMu.Unlock()
	c := baiduCookies[baiduCookieIdx]
	baiduCookieIdx = (baiduCookieIdx + 1) % len(baiduCookies)
	return c
}

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
	results, err := a.searchOnce(ctx, query, opts)
	if err != nil {
		return nil, err
	}
	if len(results) > 0 {
		return filterResults(results, opts), nil
	}

	// Retry once with different cookie + UA on empty result (rate-limit recovery)
	results, err = a.searchOnce(ctx, query, opts)
	if err != nil {
		return nil, err
	}
	return filterResults(results, opts), nil
}

func (a *BaiduAdapter) searchOnce(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("wd", query)
	params.Set("rn", strconv.Itoa(opts.MaxResults))
	params.Set("ie", "utf-8")

	reqURL := "https://www.baidu.com/s?" + params.Encode()
	md, err := fetchAndExtract(ctx, a.client, reqURL, map[string]string{"Cookie": nextBaiduCookie()})
	if err != nil {
		return nil, err
	}

	results := extractSearchResultsFromMarkdown(md, opts.MaxResults)
	for i := range results {
		if resolved := decodeBaiduRedirectURL(results[i].URL); resolved != "" {
			results[i].URL = resolved
		}
	}
	return results, nil
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
