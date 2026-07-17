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

	"github.com/DotNetAge/goharness/logging"
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

// SearchResult 表示单条网络搜索结果。
type SearchResult struct {
	Title   string `json:"title"`             // 结果标题
	URL     string `json:"url"`               // 结果 URL
	Snippet string `json:"snippet,omitempty"` // 结果摘要
	Source  string `json:"source,omitempty"`  // 来源搜索引擎
}

// SearchAdapter 是搜索适配器接口。
// 遵循 Claude Code 的适配器工厂模式，支持多个搜索引擎注册和回退。
//
// 实现此接口可以为 WebSearchTool 添加新的搜索引擎后端。
type SearchAdapter interface {
	// Name 返回适配器的标识符（如 "sogou", "weixin"）。
	Name() string
	// Search 执行网络搜索并返回结果。
	Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error)
}

// SearchOptions 配置搜索行为。
type SearchOptions struct {
	MaxResults     int      // 最大返回结果数量
	AllowedDomains []string // 允许的域名白名单
	BlockedDomains []string // 屏蔽的域名黑名单
}

// searchCacheTTL is the lifetime of cached search results (2 days).
const searchCacheTTL = 48 * time.Hour

// cacheSessionID is a virtual session ID for KVStore-scoped search cache entries.
const cacheSessionID = "__goharness_search_cache__"

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
		return "", fmt.Errorf("创建请求失败：%w", err)
	}
	req.Header.Set("User-Agent", randomUA())
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("搜索请求失败：%w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("读取响应失败：%w", err)
	}

	md, err := mdConverter.ConvertString(string(body))
	if err != nil || len(md) == 0 {
		return "", fmt.Errorf("HTML 转换为 Markdown 失败：%w", err)
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

// WebSearchTool 实现了网络搜索工具。
// 支持多引擎并行搜索和结果缓存，遵循 Claude Code 的架构设计：
//   - WebSearch: 轻量级发现 → 返回 {title, url}（小 token 成本）
//   - WebFetch: 深度读取 → 本地 HTTP 获取 → HTML→Markdown → LLM 摘要
//
// 特性：
//   - 多引擎并行搜索（默认：搜狗 + 微信）
//   - 结果去重和合并
//   - 2 天 TTL 的 KVStore 缓存
//   - 域名过滤（允许/屏蔽列表）
type WebSearchTool struct {
	adapters  []SearchAdapter // 搜索适配器列表
	adapterMu sync.RWMutex    // 适配器列表的读写锁
}

// NewWebSearchTool 创建一个 WebSearchTool 实例。
// 默认使用搜狗和微信搜索适配器的混合搜索模式。
//
// 搜索策略：
//   - 并行执行所有适配器
//   - 合并并去重结果
//   - 8 秒超时保证响应速度
//   - 结果通过 KVStore 缓存 2 天
//
// 返回：
//   - FuncTool: 配置好的 WebSearchTool 实例
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
		Description:        `搜索网络以获取实时信息。返回 {title, url} 结果列表。`,
		Prompt: `搜索网络以获取实时信息。返回搜索结果信息，格式化为搜索结果块，包含作为 markdown 超链接的链接。
为当前事件和最近的数据提供最新信息。
使用此工具访问模型知识截止日期之外的信息。
搜索在单次 API 调用中自动执行。

关键要求 - 您必须遵循以下规则：
- 在回答用户问题后，您必须在回复末尾包含"来源："部分
- 在来源部分，将所有相关的搜索结果 URL 列为 markdown 超链接：[标题](URL)
- 这是强制性的 - 永远不要在回复中跳过包含来源

使用说明：
- 支持域名过滤以包含或阻止特定网站
- 重要：在搜索查询中使用正确的年份`,
		IsReadOnly: true,
		Parameters: []Parameter{
			{
				Name:        "query",
				Type:        "string",
				Description: "搜索查询字符串。请具体并包含相关关键词。",
				Required:    true,
			},
			{
				Name:        "max_results",
				Type:        "integer",
				Description: "要返回的最大结果数（默认：5，最大：20）。提前退出阈值 - 一旦收集到足够的结果，将立即返回较快引擎的结果，而无需等待较慢的引擎。",
				Required:    false,
			},
			{
				Name:        "allowed_domains",
				Type:        "array",
				Description: "将结果限制在这些域名（例如，[\"github.com\", \"docs.python.org\"]）。",
				Required:    false,
			},
			{
				Name:        "blocked_domains",
				Type:        "array",
				Description: "排除这些域名的结果。",
				Required:    false,
			},
		},
	}
}

// searchAllAdapters 针对单个查询在所有适配器上并行搜索，合并并去重结果。
func (t *WebSearchTool) searchAllAdapters(ctx context.Context, query string, opts SearchOptions, maxResults int) ([]SearchResult, []string) {
	t.adapterMu.RLock()
	adapters := make([]SearchAdapter, len(t.adapters))
	copy(adapters, t.adapters)
	t.adapterMu.RUnlock()

	logger := getLogger(ctx)

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
				resultCh <- adapterResult{err: fmt.Errorf("上下文已取消"), adapter: a.Name()}
				return
			}

			searchResults, err := a.Search(ctx, query, opts)
			if err != nil {
				logger.Warn("search adapter failed", "adapter", a.Name(), "error", err)
				resultCh <- adapterResult{err: err, adapter: a.Name()}
				return
			}
			if len(searchResults) == 0 {
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

	// Deduplicate by URL
	seenURLs := make(map[string]bool)
	var deduped []SearchResult
	for _, r := range allResults {
		if !seenURLs[r.URL] {
			seenURLs[r.URL] = true
			deduped = append(deduped, r)
		}
	}
	if maxResults > 0 && len(deduped) > maxResults {
		deduped = deduped[:maxResults]
	}
	return deduped, failedAdapters
}

func (t *WebSearchTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	query, err := ValidateRequiredString(params, "query")
	if err != nil {
		return nil, err
	}
	if len(query) < 2 {
		return nil, fmt.Errorf("查询必须至少为 2 个字符")
	}

	maxResults := 5
	if raw, found := GetParam(params, "max_results"); found {
		if v, ok := ToFloat64(raw); ok && v > 0 {
			maxResults = int(v)
			if maxResults > 20 {
				maxResults = 20
			}
		}
	}

	var allowedDomains, blockedDomains []string
	if raw, found := GetParam(params, "allowed_domains"); found {
		if rawSlice, ok := raw.([]any); ok {
			for _, v := range rawSlice {
				if s, ok := v.(string); ok {
					allowedDomains = append(allowedDomains, s)
				}
			}
		}
	}
	if raw, found := GetParam(params, "blocked_domains"); found {
		if rawSlice, ok := raw.([]any); ok {
			for _, v := range rawSlice {
				if s, ok := v.(string); ok {
					blockedDomains = append(blockedDomains, s)
				}
			}
		}
	}

	// Check KVStore cache (2-day TTL) — 使用原始查询做缓存键
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

	logger := getLogger(ctx)

	hybridOpts := SearchOptions{
		MaxResults:     3,
		AllowedDomains: allowedDomains,
		BlockedDomains: blockedDomains,
	}

	// 将查询按空白符拆分为多个关键词，分别检索后合并去重。
	// LLM 倾向于输入空格分隔的关键词而非自然语句（如 "redis 迁移 配置"），
	// 多次查询能召回更全面的结果。
	tokens := splitQueryTokens(query)
	logger.Info("[WebSearch]开始搜索",
		"query", query,
		"tokens", tokens,
		"max_results", maxResults,
	)

	var allResults []SearchResult
	seenURLs := make(map[string]bool)
	failedAdapterSet := make(map[string]bool)

	for _, token := range tokens {
		results, failedAdapters := t.searchAllAdapters(ctx, token, hybridOpts, maxResults)
		for _, r := range results {
			if !seenURLs[r.URL] {
				seenURLs[r.URL] = true
				allResults = append(allResults, r)
			}
		}
		for _, f := range failedAdapters {
			failedAdapterSet[f] = true
		}
	}

	if maxResults > 0 && len(allResults) > maxResults {
		allResults = allResults[:maxResults]
	}

	results := allResults

	if len(results) == 0 {
		if ctx.Err() != nil {
			failedList := make([]string, 0, len(failedAdapterSet))
			for f := range failedAdapterSet {
				failedList = append(failedList, f)
			}
			return nil, fmt.Errorf("搜索超时（查询：%q）。失败的适配器：[%s]。请稍后重试。",
				query, strings.Join(failedList, ", "))
		}
		return nil, fmt.Errorf("未找到查询的结果：%q。请尝试简化您的查询。", query)
	}

	var adapterNote string
	if len(failedAdapterSet) > 0 {
		failedList := make([]string, 0, len(failedAdapterSet))
		for f := range failedAdapterSet {
			failedList = append(failedList, f)
		}
		adapterNote = fmt.Sprintf("\n\n[搜索状态] 部分引擎失败：%s", strings.Join(failedList, ", "))
	} else {
		adapterNote = fmt.Sprintf("\n\n[搜索状态] 所有引擎均成功。")
	}

	// Cache results in KVStore (2-day TTL) — 以原始查询为键
	if kvs != nil {
		if data, err := json.Marshal(cachedSearch{
			Results:   results,
			Timestamp: time.Now(),
			Query:     query,
		}); err == nil {
			kvs.Set(ctx, cacheSessionID, cacheKey, data, int(searchCacheTTL.Seconds()))
		}
	}

	formatted := formatSearchResults(query, results) + adapterNote
	logger.Info("WebSearch result",
		"query", query,
		"tokens", tokens,
		"result_count", len(results),
		"formatted_len", len(formatted),
	)
	return formatted, nil
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
	sb.WriteString("## 搜索结果\n\n")
	sb.WriteString(fmt.Sprintf("**查询**：%s\n\n", query))

	if len(results) == 0 {
		sb.WriteString("*未找到结果*\n")
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
		sb.WriteString(fmt.Sprintf("- **URL**：<%s>\n", r.URL))
		if r.Source != "" {
			sourceLabel := r.Source
			switch r.Source {
			case "sogou":
				sourceLabel = "搜狗"
			case "weixin":
				sourceLabel = "微信"
			}
			sb.WriteString(fmt.Sprintf("- **来源**：%s\n", sourceLabel))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n")
	sb.WriteString("使用 WebFetch 从上述任何 URL 获取完整内容。\n")
	sb.WriteString("注意：WebFetch 有 50,000 字符的内容预算。对于大型页面，请使用 prompt 参数定位特定部分。\n")
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
