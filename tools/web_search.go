package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// --- WebSearch Tool (Claude-style adapter pattern) ---

// SearchResult represents a single web search result.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet,omitempty"`
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

// --- DuckDuckGo Adapter (zero-config fallback, like Claude's Bing adapter) ---

// DuckDuckGoAdapter implements SearchAdapter using DuckDuckGo HTML search.
// This is the zero-configuration fallback (always available, no API key needed),
// similar to Claude Code's BingSearchAdapter.
type DuckDuckGoAdapter struct {
	client *http.Client
}

// NewDuckDuckGoAdapter creates a new DuckDuckGo search adapter.
func NewDuckDuckGoAdapter() *DuckDuckGoAdapter {
	return &DuckDuckGoAdapter{
		client: &http.Client{Timeout: 8 * time.Second},
	}
}

func (a *DuckDuckGoAdapter) Name() string { return "duckduckgo" }

func (a *DuckDuckGoAdapter) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("q", query)
	params.Set("kl", "wt-wt") // no region bias
	params.Set("num", strconv.Itoa(opts.MaxResults))

	reqURL := "https://html.duckduckgo.com/html/?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	results := parseDuckDuckGoHTML(body)
	return filterResults(results, opts), nil
}

// parseDuckDuckGoHTML extracts search results from DuckDuckGo HTML response.
// DuckDuckGo HTML results use class="result" containers with anchor links.
func parseDuckDuckGoHTML(html []byte) []SearchResult {
	content := string(html)
	var results []SearchResult

	// DuckDuckGo HTML format: each result is in a <a class="result__a" href="...">
	// and the snippet is in <a class="result__snippet">
	type resultEntry struct {
		Title   string
		URL     string
		Snippet string
	}

	// Simple parsing strategy: find all result blocks
	parts := strings.Split(content, `<a rel="nofollow" class="result__a"`)
	for _, part := range parts[1:] {
		entry := resultEntry{}

		// Extract URL from href
		if idx := strings.Index(part, `href="`); idx >= 0 {
			hrefPart := part[idx+6:]
			if end := strings.Index(hrefPart, `"`); end >= 0 {
				entry.URL = htmlUnescape(hrefPart[:end])
				// DuckDuckGo uses redirect URLs, extract actual URL
				if uddg := strings.Index(entry.URL, "uddg="); uddg >= 0 {
					encoded := entry.URL[uddg+5:]
					if amp := strings.Index(encoded, "&"); amp >= 0 {
						encoded = encoded[:amp]
					}
					if decoded, err := url.QueryUnescape(encoded); err == nil {
						entry.URL = decoded
					}
				}
			}
		}

		// Extract title (text between > and </a>)
		if gt := strings.Index(part, ">"); gt >= 0 {
			if end := strings.Index(part[gt:], "</a>"); end >= 0 {
				entry.Title = htmlUnescape(strings.TrimSpace(part[gt+1 : gt+end]))
			}
		}

		// Extract snippet
		if idx := strings.Index(part, `class="result__snippet"`); idx >= 0 {
			snippetPart := part[idx:]
			if gt := strings.Index(snippetPart, ">"); gt >= 0 {
				snippetPart = snippetPart[gt+1:]
				if end := strings.Index(snippetPart, "</a>"); end >= 0 {
					entry.Snippet = htmlUnescape(strings.TrimSpace(snippetPart[:end]))
				}
			}
		}

		if entry.Title != "" && entry.URL != "" {
			results = append(results, SearchResult{
				Title:   entry.Title,
				URL:     entry.URL,
				Snippet: entry.Snippet,
			})
		}
	}

	return results
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
// Uses parallel execution: Baidu + 360 (Haosou) + Sogou + DuckDuckGo.
// Each adapter returns top 5 results, which are then merged and deduplicated.
func NewWebSearchTool() core.FuncTool {
	t := &WebSearchTool{
		cacheTTL: 15 * time.Minute,
	}
	t.adapters = []SearchAdapter{
		NewBaiduAdapter(),
		NewHaosouAdapter(),
		NewSogouAdapter(),
		NewDuckDuckGoAdapter(),
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
	fmt.Fprintf(&sb, "Web search results for query: %q\n\n", query)

	for i, r := range results {
		fmt.Fprintf(&sb, "%d. [%s](%s)\n", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			snippet := r.Snippet
			if len([]rune(snippet)) > 200 {
				snippet = string([]rune(snippet)[:200]) + "..."
			}
			fmt.Fprintf(&sb, "   %s\n", snippet)
		}
	}

	fmt.Fprintln(&sb, "\nUse WebFetch to read the full content of any URL above.")
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
		Name:        "WebFetch",
		MaxResultSizeChars: 50000,
		Description: "Fetch and extract content from a web page. Use after WebSearch to read the actual content of a discovered URL.",
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
	content := htmlToText(string(body))
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

// htmlToText converts HTML to plain text, stripping tags and normalizing whitespace.
// This is a simplified version - a production implementation would use a proper
// HTML-to-Markdown converter (like Turndown in Claude Code).
func htmlToText(html string) string {
	// Remove script and style blocks
	html = stripTag(html, "script")
	html = stripTag(html, "style")
	html = stripTag(html, "nav")
	html = stripTag(html, "header")
	html = stripTag(html, "footer")
	html = stripTag(html, "noscript")

	// Convert common block elements to newlines
	html = blockToNewline(html, "p")
	html = blockToNewline(html, "div")
	html = blockToNewline(html, "br")
	html = blockToNewline(html, "h1")
	html = blockToNewline(html, "h2")
	html = blockToNewline(html, "h3")
	html = blockToNewline(html, "h4")
	html = blockToNewline(html, "h5")
	html = blockToNewline(html, "h6")
	html = blockToNewline(html, "li")
	html = blockToNewline(html, "tr")
	html = blockToNewline(html, "pre")
	html = blockToNewline(html, "blockquote")

	// Remove all remaining HTML tags
	result := stripAllTags(html)

	// Decode HTML entities
	result = htmlUnescape(result)

	// Normalize whitespace
	lines := strings.Split(result, "\n")
	var cleaned []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			cleaned = append(cleaned, line)
		}
	}

	return strings.Join(cleaned, "\n")
}

func stripTag(html, tag string) string {
	// Remove opening and closing tags and everything between them
	for {
		start := strings.Index(strings.ToLower(html), "<"+tag)
		if start == -1 {
			break
		}
		end := strings.Index(html[start:], ">")
		if end == -1 {
			break
		}
		closeTag := "</" + tag
		closeIdx := strings.Index(strings.ToLower(html[start:]), closeTag)
		if closeIdx == -1 {
			html = html[:start] + html[start+end+1:]
			continue
		}
		closeEnd := strings.Index(html[start+closeIdx:], ">")
		if closeEnd == -1 {
			html = html[:start] + html[start+end+1:]
			continue
		}
		html = html[:start] + html[start+closeIdx+closeEnd+1:]
	}
	return html
}

func blockToNewline(html, tag string) string {
	html = strings.ReplaceAll(html, "<"+tag+">", "\n")
	html = strings.ReplaceAll(html, "<"+tag+" ", "\n")
	html = strings.ReplaceAll(html, "</"+tag+">", "\n")
	return html
}

func stripAllTags(s string) string {
	var result strings.Builder
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
			result.WriteRune(r)
		}
	}
	return result.String()
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
