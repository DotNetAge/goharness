package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goreact/core"
	md "github.com/JohannesKaufmann/html-to-markdown"
)

// --- WebSearch Tool (Claude-style adapter pattern) ---

// SearchResult represents a single web search result.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
	Source  string `json:"source,omitempty"`
}

// SearchAdapter is the interface for web search providers.
// Following Claude Code's adapter factory pattern, multiple providers
// can be registered and the system falls back through them.
type SearchAdapter interface {
	// Name returns the adapter's identifier.
	Name() string
	// Search performs a web search and returns results.
	Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
}

// SearchOptions configures search behavior.
type SearchOptions struct {
	MaxResults     int
	AllowedDomains []string
	BlockedDomains []string
}

func htmlUnescape(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", "\"")
	s = strings.ReplaceAll(s, "&#39;", "'")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	return s
}

var mdConverter = md.NewConverter("", true, nil)

func normalizeHTML(rawHTML string) string {
	if rawHTML == "" {
		return ""
	}
	result, err := mdConverter.ConvertString(rawHTML)
	if err != nil || len(result) == 0 {
		return htmlUnescape(stripTagsFast(rawHTML))
	}
	return strings.TrimSpace(result)
}

func stripTagsFast(s string) string {
	var buf strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			buf.WriteRune(r)
		}
	}
	return buf.String()
}

func filterResults(results []SearchResult, opts SearchOptions) []SearchResult {
	if opts.MaxResults > 0 && len(results) > opts.MaxResults {
		results = results[:opts.MaxResults]
	}

	filtered := make([]SearchResult, 0, len(results))
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil {
			continue
		}
		// Check blocked domains
		blocked := false
		for _, d := range opts.BlockedDomains {
			if strings.HasSuffix(u.Hostname(), d) {
				blocked = true
				break
			}
		}
		if blocked {
			continue
		}
		// Check allowed domains (if specified)
		if len(opts.AllowedDomains) > 0 {
			allowed := false
			for _, d := range opts.AllowedDomains {
				if strings.HasSuffix(u.Hostname(), d) {
					allowed = true
					break
				}
			}
			if !allowed {
				continue
			}
		}
		filtered = append(filtered, r)
	}
	return filtered
}

// --- WebSearchTool ---

// WebSearchTool performs web searches, returning {title, url} pairs.
// Following Claude Code's architecture: search discovers URLs, WebFetch reads them.
//
// Claude Code pattern:
//   - WebSearch: lightweight discovery → returns {title, url} only (small token cost)
//   - WebFetch: deep reading → local HTTP fetch → HTML→Markdown → LLM summarization
type WebSearchTool struct {
	adapters  []SearchAdapter
	adapterMu sync.RWMutex
	cache     sync.Map // map[string]cachedSearch
	cacheTTL  time.Duration
}

type cachedSearch struct {
	results   []SearchResult
	timestamp time.Time
}

// NewWebSearchTool creates a WebSearchTool with multiple search adapters for hybrid search.
// Uses parallel execution: Baidu + 360 (Haosou) + Sogou.
// Each adapter returns top 5 results, which are then merged and deduplicated.
func NewWebSearchTool() core.FuncTool {
	t := &WebSearchTool{
		cacheTTL: 15 * time.Minute,
	}
	t.adapters = []SearchAdapter{
		NewBaiduAdapter(),
		NewHaosouAdapter(),
		NewSogouAdapter(),
	}
	return t
}

// AddAdapter adds a search adapter to the fallback chain.
// Adapters are tried in order; first successful result wins.
// This method is safe for concurrent use.
func (t *WebSearchTool) AddAdapter(adapter SearchAdapter) {
	t.adapterMu.Lock()
	defer t.adapterMu.Unlock()
	t.adapters = append([]SearchAdapter{adapter}, t.adapters...)
}

func (t *WebSearchTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name: "WebSearch",
		Description: `Search the web for real-time information. Returns a list of {title, url} results.
Use this tool when you need up-to-date information beyond your training data.`,
		Prompt: `Search the web for real-time information. Returns search result information formatted as search result blocks, including links as markdown hyperlinks.
Provides up-to-date information for current events and recent data.
Use this tool for accessing information beyond the model's knowledge cutoff.
Searches are performed automatically within a single API call.

CRITICAL REQUIREMENT - You MUST follow this:
- After answering the user's question, you MUST include a "Sources:" section at the end of your response
- In the Sources section, list all relevant URLs from the search results as markdown hyperlinks: [Title](URL)
- This is MANDATORY - never skip including sources in your response

Usage notes:
- Domain filtering is supported to include or block specific websites
- IMPORTANT: Use the correct year in search queries`,
		IsReadOnly: true,
		Parameters: []core.Parameter{
			{
				Name:        "query",
				Type:        "string",
				Description: "The search query string. Be specific and include relevant keywords.",
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
				Description: "Restrict results to these domains (e.g., [\"github.com\", \"docs.python.org\"]).",
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

func (t *WebSearchTool) Execute(ctx context.Context, params map[string]any) (any, error) {
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

	// Check cache
	cacheKey := query + "|" + strings.Join(allowedDomains, ",") + "|" + strings.Join(blockedDomains, ",")
	if cached, ok := t.cache.Load(cacheKey); ok {
		entry := cached.(cachedSearch)
		if time.Since(entry.timestamp) < t.cacheTTL {
			return formatSearchResults(query, entry.results), nil
		}
		t.cache.Delete(cacheKey)
	}

	// Hybrid parallel search: run all adapters concurrently, merge results
	// Each adapter returns top 5 results, then we merge and deduplicate
	t.adapterMu.RLock()
	adapters := make([]SearchAdapter, len(t.adapters))
	copy(adapters, t.adapters)
	t.adapterMu.RUnlock()

	logger := getLogger(ctx)

	hybridOpts := SearchOptions{
		MaxResults:     3,
		AllowedDomains: allowedDomains,
		BlockedDomains: blockedDomains,
	}

	type adapterResult struct {
		results []SearchResult
		err     error
		adapter string
	}

	resultCh := make(chan adapterResult, len(adapters))
	var wg sync.WaitGroup

	for _, adapter := range adapters {
		wg.Add(1)
		go func(a SearchAdapter) {
			defer wg.Done()

			if ctx.Err() != nil {
				resultCh <- adapterResult{err: fmt.Errorf("context cancelled"), adapter: a.Name()}
				return
			}

			searchResults, err := a.Search(ctx, query, hybridOpts)
			if err != nil {
				logger.Warn("search adapter failed in hybrid mode",
					"adapter", a.Name(),
					"error", err,
				)
				resultCh <- adapterResult{err: err, adapter: a.Name()}
				return
			}

			if len(searchResults) == 0 {
				logger.Info("search adapter returned empty results in hybrid mode",
					"adapter", a.Name(),
					"query", truncateStr(query, 50),
				)
				resultCh <- adapterResult{results: []SearchResult{}, adapter: a.Name()}
				return
			}

			resultCh <- adapterResult{results: searchResults, adapter: a.Name()}
		}(adapter)
	}

	wg.Wait()
	close(resultCh)

	var allResults []SearchResult
	var failedAdapters []string
	successCount := 0
	for result := range resultCh {
		if result.err == nil && len(result.results) > 0 {
			for i := range result.results {
				result.results[i].Source = result.adapter
			}
			allResults = append(allResults, result.results...)
			successCount++
			logger.Info("search adapter succeeded in hybrid mode",
				"adapter", result.adapter,
				"result_count", len(result.results),
			)
		} else if result.err != nil {
			failedAdapters = append(failedAdapters, fmt.Sprintf("%s (%v)", result.adapter, result.err))
		}
	}

	// Deduplicate results by URL (keep first occurrence)
	seenURLs := make(map[string]bool)
	var dedupedResults []SearchResult
	for _, r := range allResults {
		if !seenURLs[r.URL] {
			seenURLs[r.URL] = true
			dedupedResults = append(dedupedResults, r)
		}
	}

	// Apply max_results limit to final merged results
	if maxResults > 0 && len(dedupedResults) > maxResults {
		dedupedResults = dedupedResults[:maxResults]
	}

	results := dedupedResults

	if len(results) == 0 {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("search timed out after %v (query: %q). Failed adapters: [%s]. The search engine may be rate-limiting or experiencing issues. Try again later.",
				15*time.Second, query, strings.Join(failedAdapters, ", "))
		}
		return nil, fmt.Errorf("no results found for query: %q. All search engines failed: [%s]. Possible reasons: GFW blocking, rate-limiting, or network issues. Try simplifying your query.",
			query, strings.Join(failedAdapters, ", "))
	}

	var adapterNote string
	if len(failedAdapters) > 0 {
		adapterNote = fmt.Sprintf("\n\n[Search Status] %d/%d engines succeeded. Failed: %s",
			successCount, len(adapters), strings.Join(failedAdapters, ", "))
	} else {
		adapterNote = fmt.Sprintf("\n\n[Search Status] All %d engines succeeded.", len(adapters))
	}

	// Cache results
	t.cache.Store(cacheKey, cachedSearch{
		results:   results,
		timestamp: time.Now(),
	})

	return formatSearchResults(query, results) + adapterNote, nil
}

func formatSearchResults(query string, results []SearchResult) string {
	var sb strings.Builder
	sb.WriteString("## 搜索结果\n\n")
	sb.WriteString(fmt.Sprintf("**查询**: %s\n\n", query))

	if len(results) == 0 {
		sb.WriteString("*无搜索结果*\n")
		return sb.String()
	}

	for i, r := range results {
		sb.WriteString(fmt.Sprintf("### %d. %s\n\n", i+1, r.Title))
		if r.Snippet != "" {
			snippet := r.Snippet
			if len([]rune(snippet)) > 250 {
				snippet = string([]rune(snippet)[:250]) + "..."
			}
			sb.WriteString(fmt.Sprintf("> %s\n\n", snippet))
		}
		sb.WriteString(fmt.Sprintf("- **URL**: <%s>\n", r.URL))
		if r.Source != "" {
			sourceLabel := r.Source
			switch r.Source {
			case "baidu":
				sourceLabel = "百度"
			case "haosou":
				sourceLabel = "360搜索"
			case "sogou":
				sourceLabel = "搜狗"
			}
			sb.WriteString(fmt.Sprintf("- **来源**: %s\n", sourceLabel))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n")
	sb.WriteString("使用 `WebFetch` 工具可获取上述任意 URL 的完整内容。\n")
	return sb.String()
}

// --- SSRF Protection Helpers ---

// isPrivateIP checks whether an IP address belongs to a private/reserved range.
func isPrivateIP(ip net.IP) bool {
	privateRanges := []struct {
		network *net.IPNet
	}{
		{parseCIDR("127.0.0.0/8")},    // loopback
		{parseCIDR("10.0.0.0/8")},     // RFC 1918
		{parseCIDR("172.16.0.0/12")},  // RFC 1918
		{parseCIDR("192.168.0.0/16")}, // RFC 1918
		{parseCIDR("169.254.0.0/16")}, // link-local
		{parseCIDR("::1/128")},        // IPv6 loopback
		{parseCIDR("fc00::/7")},       // IPv6 ULA
		{parseCIDR("fe80::/10")},      // IPv6 link-local
		{parseCIDR("0.0.0.0/8")},      // current network
	}
	for _, r := range privateRanges {
		if r.network != nil && r.network.Contains(ip) {
			return true
		}
	}
	return false
}

func parseCIDR(s string) *net.IPNet {
	_, network, err := net.ParseCIDR(s)
	if err != nil {
		return nil
	}
	return network
}

// validateURL checks that a URL is not pointing to a private/internal address (SSRF protection).
func validateURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme: %s (only http/https allowed)", parsed.Scheme)
	}

	host := parsed.Hostname()
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("failed to resolve host %q: %w", host, err)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("access denied: URL resolves to private/internal address %s", ip)
		}
	}
	return nil
}

// --- WebFetch Tool (local fetch → content extraction) ---

// WebFetchTool implements intelligent web content fetching:
// URL validation + SSRF protection → HTTP fetch → HTML→Text extraction.
//
// Features:
// 1. Validates URL and checks for SSRF risks (blocks private IPs).
// 2. Fetches the page content locally via HTTP.
// 3. Strips HTML tags (scripts, styles, nav, etc.) for clean text.
// 4. Returns processed content with metadata for the LLM to process.
type WebFetchTool struct {
	client   *http.Client
	cache    sync.Map // map[string]cachedFetch
	cacheTTL time.Duration
}

type cachedFetch struct {
	content   string
	timestamp time.Time
}

// NewWebFetchTool creates a WebFetch tool.
func NewWebFetchTool() core.FuncTool {
	return &WebFetchTool{
		client:   &http.Client{Timeout: 15 * time.Second},
		cacheTTL: 15 * time.Minute,
	}
}

func (t *WebFetchTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:               "WebFetch",
		MaxResultSizeChars: 50000,
		Description:        "Fetch and extract content from a web page. Use after WebSearch to read the actual content of a discovered URL.",
		Prompt: `Read the full content of a specific URL. Unlike WebSearch which only returns titles and URLs, WebFetch retrieves the actual page content.

Architecture:
1. Validates URL and checks for SSRF risks (blocks private IPs).
2. Fetches the page content locally via HTTP.
3. Strips HTML tags (scripts, styles, nav, etc.) for clean text.
4. Returns the extracted content for the LLM to process.

Use this after WebSearch to read specific URLs that look relevant. The prompt parameter helps focus the extraction on relevant details.`,
		Tags:       []string{"web", "fetch", "url", "content", "http"},
		IsReadOnly: true,
		Parameters: []core.Parameter{
			{
				Name:        "url",
				Type:        "string",
				Description: "The URL to fetch content from.",
				Required:    true,
			},
			{
				Name:        "prompt",
				Type:        "string",
				Description: "What information to extract or what question to answer about the page content. Helps focus the output on relevant details.",
				Required:    false,
			},
		},
	}
}

func (t *WebFetchTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	rawURL, err := ValidateRequiredString(params, "url")
	if err != nil {
		return nil, err
	}
	prompt, _ := params["prompt"].(string)

	// Normalize URL
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "http://") {
		rawURL = "https://" + rawURL[7:]
	}
	if !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	// SSRF protection
	if err := validateURL(rawURL); err != nil {
		return nil, err
	}

	// Check cache
	if cached, ok := t.cache.Load(rawURL); ok {
		entry := cached.(cachedFetch)
		if time.Since(entry.timestamp) < t.cacheTTL {
			if prompt != "" {
				return fmt.Sprintf("--- Web Fetch: %s ---\nPrompt: %s\n\n%s", rawURL, prompt, entry.content), nil
			}
			return fmt.Sprintf("--- Web Fetch: %s ---\n\n%s", rawURL, entry.content), nil
		}
		t.cache.Delete(rawURL)
	}

	// Fetch
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024)) // 10MB limit
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	// Process content
	content := htmlToMarkdown(string(body))
	content = TruncateString(content, 50000) // 50K chars max

	// Cache
	t.cache.Store(rawURL, cachedFetch{
		content:   content,
		timestamp: time.Now(),
	})

	// Format output
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- Web Fetch: %s ---\n", rawURL)
	fmt.Fprintf(&sb, "Status: %d | Size: %d chars\n", resp.StatusCode, len(content))
	if prompt != "" {
		fmt.Fprintf(&sb, "Prompt: %s\n", prompt)
	}
	fmt.Fprintf(&sb, "\n%s\n", content)

	return sb.String(), nil
}

// htmlToMarkdown converts HTML to clean Markdown using html-to-markdown library.
func htmlToMarkdown(htmlStr string) string {
	converter := md.NewConverter("", true, nil)
	mdResult, err := converter.ConvertString(htmlStr)
	if err != nil || len(mdResult) == 0 {
		return fallbackHtmlToText(htmlStr)
	}
	var lines []string
	for _, line := range strings.Split(mdResult, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return strings.Join(lines, "\n")
}

// fallbackHtmlToText is the old stripTags-based converter used when htmltomarkdown fails.
func fallbackHtmlToText(s string) string {
	s = strings.ReplaceAll(s, "<script", "\n<script")
	s = strings.ReplaceAll(s, "</script>", "</script>\n")
	s = strings.ReplaceAll(s, "<style", "\n<style")
	s = strings.ReplaceAll(s, "</style>", "</style>\n")

	var result strings.Builder
	inTag := false
	inScriptOrStyle := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
			if len(result.String()) > 0 && result.String()[len(result.String())-1] == '\n' && !inScriptOrStyle {
				continue
			}
		case '>':
			inTag = false
			lower := strings.ToLower(strings.TrimSpace(result.String()))
			if strings.HasSuffix(lower, "script") || strings.HasSuffix(lower, "style") {
				inScriptOrStyle = true
				result.WriteRune(r)
				continue
			}
			if inScriptOrStyle && (strings.Contains(lower, "/script") || strings.Contains(lower, "/style")) {
				inScriptOrStyle = false
				result.WriteRune(r)
				continue
			}
			if !inScriptOrStyle {
				result.WriteByte(' ')
			}
			continue
		default:
			if !inTag && !inScriptOrStyle {
				result.WriteRune(r)
			} else if r == '/' && inTag {
				result.WriteRune(r)
			}
		}
	}

	cleaned := strings.Join(strings.Fields(result.String()), " ")
	var finalLines []string
	for _, line := range strings.Split(cleaned, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && len(line) > 1 {
			finalLines = append(finalLines, line)
		}
	}
	return strings.Join(finalLines, "\n")
}

// MarshalJSON for SearchResult
func (r SearchResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Snippet string `json:"snippet,omitempty"`
	}{
		Title:   r.Title,
		URL:     r.URL,
		Snippet: r.Snippet,
	})
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// getLogger extracts Logger from ToolContext or returns default slog-based logger.
// This enables dependency injection while maintaining backward compatibility.
func getLogger(ctx context.Context) core.Logger {
	tc := core.GetToolContext(ctx)
	if tc != nil && tc.Logger != nil {
		return tc.Logger
	}
	return core.DefaultLogger()
}
