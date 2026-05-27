package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goreact/logging"
	md "github.com/JohannesKaufmann/html-to-markdown"
)

var uaPool = []string{
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1",
	"Mozilla/5.0 (Linux; Android 14; Pixel 8 Pro) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.200 Mobile Safari/537.36",
	"Mozilla/5.0 (Linux; Android 14; SM-S928B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.6778.200 Mobile Safari/537.36",
}

var uaMu sync.Mutex

func randomUA() string {
	uaMu.Lock()
	defer uaMu.Unlock()
	return uaPool[rand.Intn(len(uaPool))]
}

var mdLinkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)

func extractSearchResultsFromMarkdown(md string, maxResults int) []SearchResult {
	var results []SearchResult
	seen := make(map[string]bool)

	lines := strings.Split(md, "\n")

	for i := 0; i < len(lines) && len(results) < maxResults; i++ {
		line := lines[i]

		if !strings.HasPrefix(line, "#") {
			continue
		}

		m := mdLinkRe.FindStringSubmatch(line)
		if len(m) < 3 {
			continue
		}
		title := strings.TrimSpace(m[1])
		rawURL := strings.TrimSpace(m[2])
		if title == "" || rawURL == "" || seen[rawURL] {
			continue
		}
		seen[rawURL] = true

		cleanTitle := stripMarkdownEmphasis(title)

		snippet := extractSnippet(lines, i+1)

		if !isResultQualityOK(cleanTitle, snippet, rawURL) {
			continue
		}

		results = append(results, SearchResult{
			Title:   cleanTitle,
			URL:     rawURL,
			Snippet: snippet,
		})
	}

	return results
}

var adPatterns = []string{"广告", "推广", "赞助", "ad", "sponsored", "promoted"}

func isResultQualityOK(title, snippet, rawURL string) bool {
	if len(title) < 2 || len(rawURL) < 4 {
		return false
	}
	lower := strings.ToLower(title + " " + snippet)
	for _, p := range adPatterns {
		if strings.Contains(lower, p) {
			return false
		}
	}
	return true
}

func stripMarkdownEmphasis(s string) string {
	s = strings.ReplaceAll(s, "_", "")
	s = strings.ReplaceAll(s, "*", "")
	s = strings.ReplaceAll(s, "`", "")
	return s
}

func extractSnippet(lines []string, start int) string {
	var parts []string
	for j := start; j < len(lines) && len(parts) < 3; j++ {
		l := strings.TrimSpace(lines[j])
		if l == "" {
			continue
		}
		if strings.HasPrefix(l, "#") {
			break
		}
		if mdLinkRe.MatchString(l) || strings.Contains(l, "![") {
			continue
		}
		parts = append(parts, l)
	}
	joined := strings.Join(parts, " ")
	joined = stripMarkdownEmphasis(joined)
	return strings.TrimSpace(joined)
}

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

// searchCacheTTL is the lifetime of cached search results (2 days).
const searchCacheTTL = 48 * time.Hour

// cacheSessionID is a virtual session ID for KVStore-scoped search cache entries.
const cacheSessionID = "__goreact_search_cache__"

type cachedSearch struct {
	Results   []SearchResult `json:"results"`
	Timestamp time.Time      `json:"timestamp"`
	Query     string         `json:"query"`
}

var mdConverter = md.NewConverter("", true, nil)

// Each adapter is responsible for building the request URL and resolving result URLs.
func fetchAndExtract(ctx context.Context, client *http.Client, reqURL string, extraHeaders map[string]string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", randomUA())
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	md, err := mdConverter.ConvertString(string(body))
	if err != nil || len(md) == 0 {
		return "", fmt.Errorf("failed to convert HTML to Markdown: %w", err)
	}
	return md, nil
}

func filterResults(results []SearchResult, opts SearchOptions) []SearchResult {
	if opts.MaxResults > 0 && len(results) > opts.MaxResults {
		results = results[:opts.MaxResults]
	}

	filtered := make([]SearchResult, 0, len(results))
	for _, r := range results {
		u, err := url.Parse(r.URL)
		if err != nil {
			// Allow relative paths like /link?url=..., skip truly broken URLs like ://invalid
			if strings.HasPrefix(r.URL, "/") || strings.HasPrefix(r.URL, "./") {
				filtered = append(filtered, r)
			}
			continue
		}
		if !u.IsAbs() {
			// Relative path (no scheme) — bypass hostname-based domain filtering
			filtered = append(filtered, r)
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
}

// NewWebSearchTool creates a WebSearchTool with multiple search adapters for hybrid search.
// Uses parallel execution: Baidu + Sogou + Weixin.
// Each adapter returns top results, which are then merged and deduplicated.
// Results are cached via the KVStore interface (2-day TTL) for speed and knowledge reuse.
func NewWebSearchTool() FuncTool {
	t := &WebSearchTool{}
	t.adapters = []SearchAdapter{
		NewSogouAdapter(),
		NewWeixinAdapter(),
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

func (t *WebSearchTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "WebSearch",
		MaxResultSizeChars: 30000,
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
		Parameters: []Parameter{
			{
				Name:        "query",
				Type:        "string",
				Description: "The search query string. Be specific and include relevant keywords.",
				Required:    true,
			},
			{
				Name:        "max_results",
				Type:        "integer",
				Description: "Maximum number of results to return (default: 5, max: 20). Early exit threshold — once enough results are collected, faster engines' results are returned immediately without waiting for slower ones.",
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

	maxResults := 5
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

	// Check KVStore cache (2-day TTL)
	cacheKey := query + "|" + strings.Join(allowedDomains, ",") + "|" + strings.Join(blockedDomains, ",")
	kvs := GetToolContext(ctx).KVStore
	if kvs != nil {
		if data, err := kvs.Get(ctx, cacheSessionID, cacheKey); err == nil && len(data) > 0 {
			var entry cachedSearch
			if json.Unmarshal(data, &entry) == nil && time.Since(entry.Timestamp) < searchCacheTTL {
				return formatSearchResults(query, entry.Results), nil
			}
			if data != nil {
				kvs.Delete(ctx, cacheSessionID, cacheKey)
			}
		}
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

	deadline := time.After(8 * time.Second)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	var allResults []SearchResult
	var failedAdapters []string
	successCount := 0
	collected := 0

collectLoop:
	for collected < len(adapters) {
		select {
		case result := <-resultCh:
			collected++
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
				if len(allResults) >= maxResults {
					break collectLoop
				}
			} else if result.err != nil {
				failedAdapters = append(failedAdapters, fmt.Sprintf("%s (%v)", result.adapter, result.err))
			}
		case <-done:
			break collectLoop
		case <-deadline:
			break collectLoop
		case <-ctx.Done():
			break collectLoop
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

	// Cache results in KVStore (2-day TTL)
	if kvs != nil {
		if data, err := json.Marshal(cachedSearch{
			Results:   results,
			Timestamp: time.Now(),
			Query:     query,
		}); err == nil {
			kvs.Set(ctx, cacheSessionID, cacheKey, data, int(searchCacheTTL.Seconds()))
		}
	}

	return formatSearchResults(query, results) + adapterNote, nil
}

// ---- Cache query API (knowledge reuse via KVStore) ----

// CachedQueryCount returns how many unique queries are in the KVStore cache.
func (t *WebSearchTool) CachedQueryCount(ctx context.Context) int {
	kvs := GetToolContext(ctx).KVStore
	if kvs == nil {
		return 0
	}
	keys, err := kvs.ListKeys(ctx, cacheSessionID)
	if err != nil {
		return 0
	}
	return len(keys)
}

// AllCachedQueries returns all cache keys (query strings) stored in KVStore.
func (t *WebSearchTool) AllCachedQueries(ctx context.Context) []string {
	kvs := GetToolContext(ctx).KVStore
	if kvs == nil {
		return nil
	}
	keys, err := kvs.ListKeys(ctx, cacheSessionID)
	if err != nil {
		return nil
	}
	return keys
}

// AllCachedResults returns every unique URL across all cached queries in KVStore.
func (t *WebSearchTool) AllCachedResults(ctx context.Context) []SearchResult {
	kvs := GetToolContext(ctx).KVStore
	if kvs == nil {
		return nil
	}
	keys, err := kvs.ListKeys(ctx, cacheSessionID)
	if err != nil {
		return nil
	}
	var all []SearchResult
	seen := make(map[string]bool)
	for _, key := range keys {
		data, err := kvs.Get(ctx, cacheSessionID, key)
		if err != nil || len(data) == 0 {
			continue
		}
		var entry cachedSearch
		if json.Unmarshal(data, &entry) != nil {
			continue
		}
		for _, r := range entry.Results {
			if !seen[r.URL] {
				seen[r.URL] = true
				all = append(all, r)
			}
		}
	}
	return all
}

// SearchCache searches all cached KVStore entries by keyword. Title, snippet,
// and the original query are all matched. Returns deduplicated results.
// This enables other mechanisms (memory, checks) to reuse externally
// collected knowledge without making a new network request.
func (t *WebSearchTool) SearchCache(ctx context.Context, keyword string) []SearchResult {
	kvs := GetToolContext(ctx).KVStore
	if kvs == nil {
		return nil
	}
	keys, err := kvs.ListKeys(ctx, cacheSessionID)
	if err != nil {
		return nil
	}
	kw := strings.ToLower(keyword)
	var matches []SearchResult
	seen := make(map[string]bool)
	for _, key := range keys {
		data, err := kvs.Get(ctx, cacheSessionID, key)
		if err != nil || len(data) == 0 {
			continue
		}
		var entry cachedSearch
		if json.Unmarshal(data, &entry) != nil {
			continue
		}
		if strings.Contains(strings.ToLower(entry.Query), kw) {
			for _, r := range entry.Results {
				if !seen[r.URL] {
					seen[r.URL] = true
					matches = append(matches, r)
				}
			}
			continue
		}
		for _, r := range entry.Results {
			if seen[r.URL] {
				continue
			}
			if strings.Contains(strings.ToLower(r.Title), kw) ||
				strings.Contains(strings.ToLower(r.Snippet), kw) {
				seen[r.URL] = true
				matches = append(matches, r)
			}
		}
	}
	return matches
}

// EvictExpiredCache removes entries older than the TTL from KVStore.
func (t *WebSearchTool) EvictExpiredCache(ctx context.Context) {
	kvs := GetToolContext(ctx).KVStore
	if kvs == nil {
		return
	}
	keys, err := kvs.ListKeys(ctx, cacheSessionID)
	if err != nil {
		return
	}
	now := time.Now()
	for _, key := range keys {
		data, err := kvs.Get(ctx, cacheSessionID, key)
		if err != nil || len(data) == 0 {
			kvs.Delete(ctx, cacheSessionID, key)
			continue
		}
		var entry cachedSearch
		if json.Unmarshal(data, &entry) != nil {
			kvs.Delete(ctx, cacheSessionID, key)
			continue
		}
		if now.Sub(entry.Timestamp) > searchCacheTTL {
			kvs.Delete(ctx, cacheSessionID, key)
		}
	}
}

func formatSearchResults(query string, results []SearchResult) string {
	var sb strings.Builder
	sb.WriteString("## Search Results\n\n")
	sb.WriteString(fmt.Sprintf("**Query**: %s\n\n", query))

	if len(results) == 0 {
		sb.WriteString("*No results found*\n")
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
			case "sogou":
				sourceLabel = "Sogou"
			case "weixin":
				sourceLabel = "WeChat"
			}
			sb.WriteString(fmt.Sprintf("- **Source**: %s\n", sourceLabel))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n")
	sb.WriteString("Use WebFetch to retrieve full content from any URL above.\n")
	sb.WriteString("Note: WebFetch has a 50,000 character content budget. For large pages, use the prompt parameter to target specific sections.\n")
	return sb.String()
}

// --- SSRF Protection Helpers ---

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
func getLogger(ctx context.Context) logging.Logger {
	tc := GetToolContext(ctx)
	if tc != nil && tc.Logger != nil {
		return tc.Logger
	}
	return logging.DefaultLogger()
}
