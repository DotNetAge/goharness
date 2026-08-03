package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	md "github.com/JohannesKaufmann/html-to-markdown"
	"github.com/PuerkitoBio/goquery"
)

// newSearchStealthClient 创建搜索用的隐蔽客户端。
// logger 必须由调用方（适配器 → WebSearchTool → Runtime）注入，禁止内部创建日志
// （项目硬性约束）。运行时请求日志另由 stealthClient.requestLogger 从 ctx 取
// ToolContext.Logger；此 logger 仅用于构造期告警与 tls-client debug。
func newSearchStealthClient(logger logging.Logger, timeout time.Duration) *stealthClient {
	// NewStealthClient 仅在 logger==nil 时返回 error；logger 由 Runtime → WebSearchTool →
	// 适配器逐级注入，此处必非 nil，故 c 必非 nil、err 必 nil。不构建 NopLogger 兜底——
	// 那是废日志，违背「禁止内部创建日志」约束；契约违反应让 NewStealthClient 自然暴露，
	// 而非用废日志掩盖成黑箱。
	c, _ := NewStealthClient(logger, WithTimeout(timeout))
	return c
}

// extractResultsWithGoquery 用 CSS 选择器从原始 HTML 提取搜索结果。
// 相比手搓 markdown 正则，goquery 保留了 class/属性等结构信息，能精准定位
// 各引擎的主结果块，避免把侧边栏/导航垃圾当结果。
//   - selector: 结果标题链接的 CSS 选择器（如必应 "li.b_algo h2 a"）
//   - urlAttr: 从 a 标签的哪个属性取 URL（"href" 或 "data-mdurl"）
//   - base: 相对 URL 的补全基准（如 "https://www.sogou.com"）
//   - resolveURL: 解析重定向链接的函数（如搜狗/微信的 /link?url=），无重定向传 nil
func extractResultsWithGoquery(html, selector, urlAttr, base string, resolveURL func(string) string, maxResults int) []SearchResult {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(html))
	if err != nil {
		return nil
	}
	var results []SearchResult
	seen := make(map[string]bool)
	doc.Find(selector).Each(func(_ int, s *goquery.Selection) {
		if len(results) >= maxResults {
			return
		}
		// goquery 的 Text() 已自动剥离 <em>/<strong> 等标签，标题天然干净
		title := strings.TrimSpace(s.Text())
		rawURL, ok := s.Attr(urlAttr)
		if !ok || rawURL == "" || title == "" {
			return
		}
		finalURL := rawURL
		if resolveURL != nil {
			if u := resolveURL(rawURL); u != "" {
				finalURL = u
			}
		}
		// 相对 URL 补全（resolveURL 未处理的兜底）
		if strings.HasPrefix(finalURL, "/") {
			finalURL = base + finalURL
		}
		if !strings.HasPrefix(finalURL, "http://") && !strings.HasPrefix(finalURL, "https://") {
			return
		}
		if seen[finalURL] {
			return
		}
		seen[finalURL] = true
		if !isResultQualityOK(title, "", finalURL) {
			return
		}
		results = append(results, SearchResult{
			Title: title,
			URL:   finalURL,
		})
	})
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
	MaxResults int // 最大返回结果数量
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

// fetchBody 发起搜索请求并返回原始 HTML body。
// 通过 stealthClient 统一处理 TLS 指纹伪装、浏览器指纹头注入、cookiejar 会话、
// 重试退避；沙箱 SSRF 预检在此保留（拨号层防护由 stealthClient 的 ssrfDialContext 提供）。
// 供需要原始 HTML 的适配器使用（如 360 适配器需提取 data-mdurl 里的真实 URL）。
func fetchBody(ctx context.Context, client *stealthClient, reqURL string, extraHeaders map[string]string) (string, error) {
	// 沙箱启用时，用 CheckURL 做 SSRF 预检（含 DNS 解析与网段检查）。
	// 搜索引擎 URL 是固定域名（如 https://www.sogou.com/web?...），解析到公网 IP 时 CheckURL 放行；
	// 若沙箱策略禁止了该搜索引擎的网段则拒绝。沙箱未启用时跳过，由旧逻辑兜底。
	if tc := GetToolContext(ctx); tc != nil && tc.Session != nil {
		if sb := tc.Session.Sandbox(); sb != nil {
			dec := sb.CheckURL(reqURL)
			if dec.Decision == sandbox.DecisionDeny {
				return "", fmt.Errorf("%s", dec.Reason)
			}
		}
	}

	headers := make(map[string][]string, len(extraHeaders))
	for k, v := range extraHeaders {
		headers[k] = []string{v}
	}
	resp, err := client.Do(ctx, StealthRequest{
		Method:          "GET",
		URL:             reqURL,
		Headers:         headers,
		FollowRedirects: true,
	})
	if err != nil {
		return "", fmt.Errorf("搜索请求失败：%w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("读取响应失败：%w", err)
	}
	// 风控检测：状态码 403/429/503 或响应体含风控特征短语时报错，
	// 使适配器进入 failedAdapterSet，被错误分类逻辑如实报告“搜索失败”而非“没有结果”。
	// 修复背景：原逻辑下风控页被 goquery 解析出 0 条结果，适配器返回 (nil,nil)，
	// 既无结果也无错误，被当成“没有搜索结果”——掩盖了被风控的事实。
	if err := checkAntiCrawl(resp.StatusCode, body); err != nil {
		return "", err
	}
	return string(body), nil
}

// riskKeywords 是国内搜索引擎风控页的特征短语。
// 这些短语不会出现在正常搜索结果页中，命中任一即判定被风控拦截。
var riskKeywords = []string{
	"百度安全验证",
	"人机验证",
	"请完成验证",
	"访问验证",
	"异常访问",
	"请求已被拦截",
	"为了您的安全",
	"captcha",
}

// checkAntiCrawl 检测响应是否被风控拦截。
// 触发条件：风控状态码（403/429/503）或响应体含风控特征短语。
func checkAntiCrawl(statusCode int, body []byte) error {
	if statusCode == 403 || statusCode == 429 || statusCode == 503 {
		return fmt.Errorf("搜索引擎返回风控状态码 %d", statusCode)
	}
	lower := strings.ToLower(string(body))
	for _, kw := range riskKeywords {
		if strings.Contains(lower, kw) {
			return fmt.Errorf("搜索引擎返回风控页面（命中特征：%s）", kw)
		}
	}
	return nil
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
func NewWebSearchTool(logger logging.Logger) FuncTool {
	t := &WebSearchTool{}
	t.adapters = []SearchAdapter{
		NewSogouAdapter(logger),
		NewWeixinAdapter(logger),
		NewBingAdapter(logger),
		NewSo360Adapter(logger),
		NewToutiaoAdapter(logger), // 头条独立于搜狗系风控，结果丰富，作为重要兜底引擎
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

关键要求 - 您必须遵循以下规则：
- 在回答用户问题后，您必须在回复末尾包含"来源："部分
- 在来源部分，将所有相关的搜索结果 URL 列为 markdown 超链接：[标题](URL)
- 这是强制性的 - 永远不要在回复中跳过包含来源

使用说明：
- 多个关键词用逗号分隔（如 "Go 语言,Redis"），单个主题直接用自然语句
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
				Description: "要返回的最大结果数（默认：10，最大：20）。",
				Required:    false,
			},
		},
	}
}

// searchAllAdapters 针对单个查询在所有适配器上并行搜索，合并并去重结果。
// 返回值：去重后的结果、失败引擎列表（"name (err)" 格式）、allFailed（所有引擎是否都明确失败）。
// allFailed=true 表示"全部兜底策略被打穿"——只有此时才应告知 Agent 被风控；
// 部分失败时 allFailed=false（其余引擎可能超时或无匹配，不算全部打穿）。
func (t *WebSearchTool) searchAllAdapters(ctx context.Context, query string, opts SearchOptions, maxResults int) ([]SearchResult, []string, bool) {
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
	// allFailed: 所有引擎都明确失败（报错），即"全部兜底被打穿"。
	// 超时未响应的引擎不在 failedAdapters 里，故 len(failedAdapters) < len(adapters)，
	// allFailed 自然为 false——不算全部打穿。
	allFailed := len(adapters) > 0 && len(failedAdapters) == len(adapters)
	return deduped, failedAdapters, allFailed
}

func (t *WebSearchTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	query, err := ValidateRequiredString("WebSearch", params, "query")
	if err != nil {
		return nil, err
	}
	if len(query) < 2 {
		return nil, fmt.Errorf("%s", GuideInvalidValue("WebSearch", "query", query, "提供至少 2 个字符的具体关键词（可使用组合词或英文关键词）后重试"))
	}

	maxResults := 10
	if raw, found := GetParam(params, "max_results"); found {
		if v, ok := ToFloat64(raw); ok && v > 0 {
			maxResults = int(v)
			if maxResults > 20 {
				maxResults = 20
			}
		}
	}

	// Check KVStore cache (2-day TTL) — 使用原始查询做缓存键
	cacheKey := query
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

	// 每引擎每关键词最多贡献 5 条。配合提前退出阈值（默认 10）：
	//   - 2 个稳定引擎即可凑够 10 条（5×2=10），应对搜狗/微信被风控的场景
	//   - 3 引擎全成功时 5×3=15 取前 10，多引擎多样性自然保障
	hybridOpts := SearchOptions{
		MaxResults: 5,
	}

	// 将查询按逗号拆分为多个关键词，分别检索后合并去重。
	// 逗号是显式分隔符，避免误拆 "Go 语言" 这类中英文混排的自然空格。
	// LLM 用逗号分隔多主题（如 "redis,迁移方案"），自然语句不拆分。
	tokens := splitQueryTokens(query)
	logger.Info("[WebSearch]开始搜索",
		"query", query,
		"tokens", fmt.Sprintf("%q", tokens),
		"max_results", maxResults,
	)

	var allResults []SearchResult
	seenURLs := make(map[string]bool)
	failedAdapterSet := make(map[string]bool)
	allTokensAllFailed := true // 所有 token 的所有引擎都失败才为 true（全部兜底被打穿）

	for _, token := range tokens {
		results, failedAdapters, allFailed := t.searchAllAdapters(ctx, token, hybridOpts, maxResults)
		for _, r := range results {
			if !seenURLs[r.URL] {
				seenURLs[r.URL] = true
				allResults = append(allResults, r)
			}
		}
		for _, f := range failedAdapters {
			failedAdapterSet[f] = true
		}
		if !allFailed {
			allTokensAllFailed = false
		}
	}

	// 注意：最终结果不做 maxResults 截断——条数限制已由每次关键词搜索
	// （searchAllAdapters）分别应用。最终结果是所有关键词搜索结果的合并去重，
	// 每个关键词最多贡献 maxResults 条，多关键词能够获得更全面的召回。
	results := allResults

	if len(results) == 0 {
		failedList := make([]string, 0, len(failedAdapterSet))
		for f := range failedAdapterSet {
			failedList = append(failedList, f)
		}

		// 全部兜底策略被打穿：所有引擎都明确失败（风控/网络错误/解析失败）。
		// 只有此时才告知 Agent “被风控”，避免部分失败就报错——
		// 容错优先：只要有任一引擎返回数据就正常返回，不算打穿。
		if allTokensAllFailed && len(failedList) > 0 {
			return nil, fmt.Errorf("%s", BuildGuide(
				fmt.Sprintf("搜索查询 %q 失败：所有搜索引擎均报错，全部兜底策略被打穿。失败引擎：%s", query, strings.Join(failedList, ", ")),
				"所有搜索引擎均无法完成搜索（风控/网络错误/解析失败）",
				"稍后重试；若持续失败，说明当前搜索源全部不可达，应告知用户",
			))
		}

		// 部分引擎失败，但未全部打穿（其余超时或无匹配）——不说“被风控”，如实说明部分失败
		if len(failedList) > 0 {
			return nil, fmt.Errorf("%s", BuildGuide(
				fmt.Sprintf("搜索查询 %q 未获得结果：部分引擎失败，其余超时或无匹配。失败引擎：%s", query, strings.Join(failedList, ", ")),
				"部分搜索引擎失败，未获得任何结果",
				"稍后重试；或更换关键词重新搜索",
			))
		}

		// 无引擎报错但无结果：上下文超时（8 秒 deadline 或外层取消）
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%s", BuildGuide(
				fmt.Sprintf("搜索查询 %q，但 8 秒内未获得任何结果", query),
				fmt.Sprintf("搜索超时（查询：%q）", query),
				"缩短查询词或更换关键词后重试；若持续超时，说明当前搜索源不可达，应告知用户",
			))
		}

		// 所有引擎都成功返回但无匹配结果——这才是真正的“没有搜索结果”
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("搜索查询 %q，但未找到任何搜索结果", query),
			"没有搜索引擎返回与查询匹配的结果",
			"更换关键词或使用更通用的表述重新搜索；若多次尝试仍无结果，基于已有信息直接作答",
		))
	}

	var adapterNote string
	if len(failedAdapterSet) > 0 {
		failedList := make([]string, 0, len(failedAdapterSet))
		for f := range failedAdapterSet {
			failedList = append(failedList, f)
		}
		adapterNote = fmt.Sprintf("\n\n[搜索状态] 部分引擎失败：%s", strings.Join(failedList, ", "))
	} else {
		adapterNote = "\n\n[搜索状态] 所有引擎均成功。"
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
			case "bing":
				sourceLabel = "必应"
			case "360":
				sourceLabel = "360"
			case "toutiao":
				sourceLabel = "头条"
			}
			sb.WriteString(fmt.Sprintf("- **来源**：%s\n", sourceLabel))
		}
		sb.WriteString("\n")
	}

	sb.WriteString("---\n")
	sb.WriteString("使用 WebFetch 从上述任何 URL 获取完整内容。\n")
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

// getLogger 从 ToolContext 取系统注入的 Logger。
// 生产环境下 ToolContext.Logger 必然非空：rt.logger 默认非 nil（runtime.go:220），
// 经 executor.go:44/220/91 注入到 ToolContext.Logger。故此处不再用 DefaultLogger 兜底——
// 那是内部创建日志，违背「禁止内部创建日志」约束，也属多余（ToolContext.Logger 必然非空）。
// 裸调用（无 ToolContext）属用法错误，应让 nil 暴露而非掩盖。
func getLogger(ctx context.Context) logging.Logger {
	return GetToolContext(ctx).Logger
}
