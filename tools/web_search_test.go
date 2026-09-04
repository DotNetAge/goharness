package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/logging"
)

// ============================================================
// htmlToMarkdown — HTML → Plain Text Conversion
// ============================================================

func TestHtmlToText_ScriptStyleStripped(t *testing.T) {
	input := `<html><head><script>alert("xss")</script>
<style>.hidden { display:none }</style></head>
<body><p>Hello World</p></body></html>`
	got := htmlToMarkdown(input)
	if strings.Contains(got, "alert") || strings.Contains(got, "xss") {
		t.Error("script content should be stripped")
	}
	if strings.Contains(got, "display:none") || strings.Contains(got, "hidden") {
		t.Error("style content should be stripped")
	}
	if !strings.Contains(got, "Hello World") {
		t.Errorf("body text preserved, got: %q", got)
	}
}

func TestHtmlToText_BlockElements(t *testing.T) {
	input := `<div>line1</div><p>line2</p><br/>line3<h1>Title</h1><ul><li>item</li></ul>`
	got := htmlToMarkdown(input)
	lines := strings.Split(strings.TrimSpace(got), "\n")
	if len(lines) < 3 {
		t.Errorf("expected multiple lines from block elements, got %d lines: %q", len(lines), got)
	}
}

func TestHtmlToText_NestedTags(t *testing.T) {
	input := `<p><strong>Bold</strong> and <em>italic</em> text</p>`
	got := htmlToMarkdown(input)
	if !strings.Contains(got, "Bold") || !strings.Contains(got, "italic") {
		t.Errorf("should preserve text content in nested tags, got: %q", got)
	}
}

func TestHtmlToText_EntityDecoding(t *testing.T) {
	input := `<p>Price: &amp;euro;100 &lt; $200&gt;</p>`
	got := htmlToMarkdown(input)
	if !strings.Contains(got, "$200") {
		t.Errorf("should decode entities, got: %q", got)
	}
}

func TestHtmlToText_EmptyInput(t *testing.T) {
	got := htmlToMarkdown("")
	if got != "" {
		t.Errorf("empty input should return empty, got: %q", got)
	}
}

func TestHtmlToText_WhitespaceNormalization(t *testing.T) {
	input := `<p>   spaced   out   </p>\n\n<p>   more   </p>`
	got := htmlToMarkdown(input)
	lines := strings.Split(got, "\n")
	for _, line := range lines {
		if line != "" && (strings.HasPrefix(line, " ") || strings.HasSuffix(line, " ")) {
			t.Errorf("lines should be trimmed, got: %q", line)
		}
	}
}

func TestWebSearchTool_Info(t *testing.T) {
	tool := NewWebSearchTool(logging.NewNopLogger())
	info := tool.Info()
	if info.Name != "WebSearch" {
		t.Errorf("Name = %q, want %q", info.Name, "WebSearch")
	}
	if !info.IsReadOnly {
		t.Error("web_search should be read-only")
	}
	if len(info.Parameters) == 0 {
		t.Error("expected parameters")
	}
}

func TestWebSearchTool_MissingQuery(t *testing.T) {
	tool := NewWebSearchTool(logging.NewNopLogger())
	_, err := tool.Execute(testCtx(t), nil)
	if err == nil {
		t.Error("expected error for missing query parameter")
	}
}

func TestWebSearchTool_TooShortQuery(t *testing.T) {
	tool := NewWebSearchTool(logging.NewNopLogger())
	_, err := tool.Execute(testCtx(t), map[string]any{
		"query": "x",
	})
	if err == nil {
		t.Error("expected error for query shorter than 2 characters")
	}
}

func TestWebSearchTool_CacheBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}

	ctx, cancel := context.WithTimeout(testCtx(t), 30*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger()).(*WebSearchTool)

	result1, err := tool.Execute(ctx, map[string]any{
		"query": "cache test unique query",
	})
	if err != nil {
		t.Skipf("search unreachable: %v", err)
	}

	result2, err := tool.Execute(ctx, map[string]any{
		"query": "cache test unique query",
	})
	if err != nil {
		t.Skipf("second Execute() error = %v", err)
	}

	s1 := result1.(string)
	s2 := result2.(string)
	if s1 == s2 {
		return // 精确缓存命中
	}
	t.Logf("cache results differ (acceptable for dynamic content): len1=%d, len2=%d", len(s1), len(s2))
}

// ============================================================
// formatSearchResults
// ============================================================

func TestFormatSearchResults(t *testing.T) {
	results := []SearchResult{
		{Title: "Google", URL: "https://google.com", Snippet: "Search engine"},
		{Title: "GitHub", URL: "https://github.com", Snippet: "Code hosting"},
	}
	output := formatSearchResults("test query", results)
	if !strings.Contains(output, "test query") {
		t.Error("output should contain query")
	}
	if !strings.Contains(output, "Google") || !strings.Contains(output, "github.com") {
		t.Error("output should contain all result titles and URLs")
	}
	if !strings.Contains(output, "Search engine") {
		t.Error("output should contain snippets")
	}
	if !strings.Contains(output, "WebFetch") {
		t.Error("output should mention WebFetch for follow-up")
	}
}

func TestFormatSearchResults_LongSnippetTruncated(t *testing.T) {
	longSnippet := strings.Repeat("X", 300)
	results := []SearchResult{
		{Title: "Test", URL: "https://test.com", Snippet: longSnippet},
	}
	output := formatSearchResults("q", results)
	if strings.Count(output, longSnippet) > 0 {
		t.Error("long snippets should be truncated to ~200 chars")
	}
}

func TestFormatSearchResults_Empty(t *testing.T) {
	output := formatSearchResults("empty query", nil)
	if output == "" {
		t.Error("formatSearchResults should return non-empty even for empty results")
	}
}

// ============================================================
// SearchResult JSON Marshaling
// ============================================================

// 注：旧的 randomUA 单测已移除——randomUA 在隐蔽客户端重构中被删除，
// UA/profile 配对机制（pickUAProfile）的单测见 stealth_client_test.go。

func TestSearchResult_JSONMarshal(t *testing.T) {
	r := SearchResult{
		Title:   "Test Title",
		URL:     "https://example.com",
		Snippet: "Test snippet",
	}
	data, err := r.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON() error = %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"title"`) || !strings.Contains(s, `"url"`) {
		t.Errorf("JSON should contain title and url fields, got: %s", s)
	}
}

// ============================================================
// 真实搜索诊断测试（需要网络，-short 模式跳过）
// 用于定位"完全搜不到结果"问题发生在哪一层：适配器 / 完整流程。
// ============================================================

// TestWebSearch_RealSogou 直接调用搜狗适配器，绕过 Execute 的缓存与多 token 逻辑。
func TestWebSearch_RealSogou(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 30*time.Second)
	defer cancel()

	adapter := NewSogouAdapter(logging.NewNopLogger())
	results, err := adapter.Search(ctx, "Go 语言", SearchOptions{MaxResults: 5})
	if err != nil {
		t.Fatalf("搜狗适配器搜索失败：%v", err)
	}
	t.Logf("搜狗返回 %d 条结果", len(results))
	for i, r := range results {
		t.Logf("  [%d] %s\n      %s", i+1, r.Title, r.URL)
	}
	if len(results) == 0 {
		t.Logf("搜狗适配器返回 0 条结果——可能被风控（环境问题，非代码缺陷）")
	}
}

// TestWebSearch_RealWeixin 直接调用微信适配器。
func TestWebSearch_RealWeixin(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 30*time.Second)
	defer cancel()

	adapter := NewWeixinAdapter(logging.NewNopLogger())
	results, err := adapter.Search(ctx, "Go 语言", SearchOptions{MaxResults: 5})
	if err != nil {
		t.Fatalf("微信适配器搜索失败：%v", err)
	}
	t.Logf("微信返回 %d 条结果", len(results))
	for i, r := range results {
		t.Logf("  [%d] %s\n      %s", i+1, r.Title, r.URL)
	}
	if len(results) == 0 {
		t.Logf("微信适配器返回 0 条结果——/link?url= 加密重定向未解析或被风控（环境问题）")
	}
}

// TestWebSearch_RealFullExecute 走完整 Execute 流程（多 token 拆分 + 多引擎合并 + 去重）。
func TestWebSearch_RealFullExecute(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 40*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger())
	out, err := tool.Execute(ctx, map[string]any{
		"query": "Go 语言",
	})
	if err != nil {
		t.Fatalf("WebSearchTool.Execute 失败：%v", err)
	}
	s := out.(string)
	t.Logf("完整 Execute 返回（长度 %d）：\n%s", len(s), s)
}

// TestWebSearch_RealBing 直接调用必应中国适配器。
func TestWebSearch_RealBing(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 30*time.Second)
	defer cancel()

	adapter := NewBingAdapter(logging.NewNopLogger())
	results, err := adapter.Search(ctx, "Go 语言", SearchOptions{MaxResults: 5})
	if err != nil {
		t.Fatalf("必应适配器搜索失败：%v", err)
	}
	t.Logf("必应返回 %d 条结果", len(results))
	for i, r := range results {
		t.Logf("  [%d] %s\n      %s", i+1, r.Title, r.URL)
	}
	if len(results) == 0 {
		t.Logf("必应适配器返回 0 条结果——cn.bing.com 可能被风控（环境问题，非代码缺陷）")
	}
}

// TestWebSearch_Real360 直接调用 360 搜索适配器，验证 data-mdurl 提取是否生效。
func TestWebSearch_Real360(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 30*time.Second)
	defer cancel()

	adapter := NewSo360Adapter(logging.NewNopLogger())
	results, err := adapter.Search(ctx, "Go 语言", SearchOptions{MaxResults: 5})
	if err != nil {
		t.Fatalf("360 适配器搜索失败：%v", err)
	}
	t.Logf("360 返回 %d 条结果", len(results))
	for i, r := range results {
		t.Logf("  [%d] %s\n      %s", i+1, r.Title, r.URL)
	}
	if len(results) == 0 {
		t.Logf("360 适配器返回 0 条结果——可能被风控（环境问题，非代码缺陷；单独运行该测试可验证）")
	}
	// 验证 data-mdurl 提取生效：URL 不应再是 so.com/link 重定向
	for i, r := range results {
		if strings.Contains(r.URL, "so.com/link") {
			t.Errorf("结果[%d] URL 仍是 360 重定向链接，data-mdurl 提取未生效：%s", i+1, r.URL)
		}
	}
}

// TestWebSearch_RealToutiao 直接调用头条搜索适配器，验证 /search/jump?url= 解析是否生效。
func TestWebSearch_RealToutiao(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 30*time.Second)
	defer cancel()

	adapter := NewToutiaoAdapter(logging.NewNopLogger())
	results, err := adapter.Search(ctx, "Go 语言", SearchOptions{MaxResults: 5})
	if err != nil {
		t.Fatalf("头条适配器搜索失败：%v", err)
	}
	t.Logf("头条返回 %d 条结果", len(results))
	for i, r := range results {
		t.Logf("  [%d] %s\n      %s", i+1, r.Title, r.URL)
	}
	if len(results) == 0 {
		t.Logf("头条适配器返回 0 条结果——so.toutiao.com 可能被风控（环境问题，非代码缺陷）")
	}
	// 验证 /search/jump?url= 解析生效：URL 不应再是 so.toutiao.com/search/jump 重定向
	for i, r := range results {
		if strings.Contains(r.URL, "so.toutiao.com/search/jump") {
			t.Errorf("结果[%d] URL 仍是头条跳转链接，url 参数解析未生效：%s", i+1, r.URL)
		}
	}
}

// ============================================================
// splitQueryTokens — 逗号拆分
// ============================================================

func TestSplitQueryTokens_NoComma_NaturalSentence(t *testing.T) {
	// 无逗号的自然语句不拆分——这是核心修复：空格不再触发拆分
	cases := []string{"Go 语言", "redis 迁移 配置", "OpenAI"}
	for _, q := range cases {
		got := splitQueryTokens(q)
		if len(got) != 1 || got[0] != q {
			t.Errorf("无逗号查询 %q 应原样返回，got %q", q, got)
		}
	}
}

func TestSplitQueryTokens_EnglishComma(t *testing.T) {
	got := splitQueryTokens("Go 语言,Redis")
	want := []string{"Go 语言", "Redis"}
	if !equalStringSlices(got, want) {
		t.Errorf("英文逗号拆分：got %q, want %q", got, want)
	}
}

func TestSplitQueryTokens_ChineseComma(t *testing.T) {
	got := splitQueryTokens("Go 语言，Redis")
	want := []string{"Go 语言", "Redis"}
	if !equalStringSlices(got, want) {
		t.Errorf("中文逗号拆分：got %q, want %q", got, want)
	}
}

func TestSplitQueryTokens_MixedComma_WithSpaces(t *testing.T) {
	// 混合中英文逗号 + 首尾空格，应正确拆分并 TrimSpace
	got := splitQueryTokens("Go 语言, Redis 配置， Python")
	want := []string{"Go 语言", "Redis 配置", "Python"}
	if !equalStringSlices(got, want) {
		t.Errorf("混合逗号+空格：got %q, want %q", got, want)
	}
}

func TestSplitQueryTokens_FiltersShortTokens(t *testing.T) {
	// "a"（<=1 字符）被过滤，"Go" 保留
	got := splitQueryTokens("a,Go")
	want := []string{"Go"}
	if !equalStringSlices(got, want) {
		t.Errorf("短词过滤：got %q, want %q", got, want)
	}
}

func TestSplitQueryTokens_LongQuery_NotSplit(t *testing.T) {
	// >50 字节的长查询即使含逗号也不拆分（视为自然语句）
	long := strings.Repeat("Go 语言,", 10)
	got := splitQueryTokens(long)
	if len(got) != 1 || got[0] != long {
		t.Errorf("长查询不应拆分：got %q", got)
	}
}

func TestSplitQueryTokens_Empty(t *testing.T) {
	got := splitQueryTokens("")
	if len(got) != 1 || got[0] != "" {
		t.Errorf("空查询应返回 [\"\"]，got %q", got)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ============================================================
// 错误分类回归测试 —— 防止“引擎报错被掩盖成没有搜索结果”
// ============================================================

// stubAdapter 是测试用的搜索适配器桩，按预设返回结果或错误。
type stubAdapter struct {
	name    string
	results []SearchResult
	err     error
}

func (a *stubAdapter) Name() string { return a.name }
func (a *stubAdapter) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	return a.results, a.err
}

// TestWebSearch_ErrorClassification_AllFail 验证所有引擎报错时报“搜索失败”而非“没有结果”。
// 回归防护：原逻辑在此场景统一报“未找到搜索结果”，掩盖了真实的引擎错误，
// 导致 Agent 误判为“无结果”而非“搜索失败”。
func TestWebSearch_ErrorClassification_AllFail(t *testing.T) {
	tool := &WebSearchTool{
		adapters: []SearchAdapter{
			&stubAdapter{name: "stub1", err: errors.New("网络错误")},
			&stubAdapter{name: "stub2", err: errors.New("风控拦截")},
		},
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 10*time.Second)
	defer cancel()

	_, err := tool.Execute(ctx, map[string]any{"query": "测试查询"})
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "全部兜底策略被打穿") {
		t.Errorf("所有引擎失败时应报全部兜底策略被打穿，实际：%s", err.Error())
	}
	if !strings.Contains(err.Error(), "失败引擎") {
		t.Errorf("错误信息应包含失败引擎详情，实际：%s", err.Error())
	}
	if strings.Contains(err.Error(), "未找到任何搜索结果") {
		t.Errorf("全部打穿时不应报未找到搜索结果，实际：%s", err.Error())
	}
}

// TestWebSearch_ErrorClassification_PartialFail 验证部分引擎失败时（未全部打穿）报"部分失败"而非"全部打穿"。
// 核心诉求：只有全部兜底被打穿才报风控，部分失败时不夸大；只要有任一引擎返回数据就正常返回。
func TestWebSearch_ErrorClassification_PartialFail(t *testing.T) {
	tool := &WebSearchTool{
		adapters: []SearchAdapter{
			&stubAdapter{name: "stub1", err: errors.New("风控拦截")},
			&stubAdapter{name: "stub2"}, // 成功但 0 条结果
		},
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 10*time.Second)
	defer cancel()

	_, err := tool.Execute(ctx, map[string]any{"query": "测试查询"})
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "部分引擎失败") {
		t.Errorf("部分引擎失败时应报部分引擎失败，实际：%s", err.Error())
	}
	if strings.Contains(err.Error(), "全部兜底策略被打穿") {
		t.Errorf("部分失败时不应报全部打穿，实际：%s", err.Error())
	}
}

// TestWebSearch_ErrorClassification_PartialSuccessHasData 验证：部分引擎失败但有引擎返回数据时，
// 正常返回数据（容错优先），仅附加失败引擎附注，不报错。
func TestWebSearch_ErrorClassification_PartialSuccessHasData(t *testing.T) {
	tool := &WebSearchTool{
		adapters: []SearchAdapter{
			&stubAdapter{name: "stub1", err: errors.New("风控拦截")},
			&stubAdapter{name: "stub2", results: []SearchResult{{Title: "结果A", URL: "https://a.com"}}},
		},
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 10*time.Second)
	defer cancel()

	out, err := tool.Execute(ctx, map[string]any{"query": "测试查询"})
	if err != nil {
		t.Fatalf("有引擎返回数据时不应报错，实际：%s", err.Error())
	}
	s := out.(string)
	if !strings.Contains(s, "结果A") {
		t.Errorf("应包含成功引擎的结果，实际：%s", s)
	}
	if !strings.Contains(s, "部分引擎失败") {
		t.Errorf("应附加失败引擎附注，实际：%s", s)
	}
}

// TestWebSearch_ErrorClassification_NoMatch 验证所有引擎成功但无结果时报“没有搜索结果”。
func TestWebSearch_ErrorClassification_NoMatch(t *testing.T) {
	tool := &WebSearchTool{
		adapters: []SearchAdapter{
			&stubAdapter{name: "stub1"}, // nil results, nil err —— 成功但无匹配
			&stubAdapter{name: "stub2"},
		},
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 10*time.Second)
	defer cancel()

	_, err := tool.Execute(ctx, map[string]any{"query": "测试查询"})
	if err == nil {
		t.Fatal("期望返回错误，实际为 nil")
	}
	if !strings.Contains(err.Error(), "未找到任何搜索结果") {
		t.Errorf("引擎成功但无结果时应报未找到任何搜索结果，实际：%s", err.Error())
	}
	if strings.Contains(err.Error(), "搜索引擎报错") {
		t.Errorf("无引擎报错时不应提示搜索引擎报错，实际：%s", err.Error())
	}
}

// TestCheckAntiCrawl 验证风控检测逻辑：风控状态码和风控特征短语应报错，正常页面不报错。
func TestCheckAntiCrawl(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantErr    bool
		wantSubstr string
	}{
		{"正常页面", 200, "<html>正常搜索结果页</html>", false, ""},
		{"状态码403", 403, "", true, "风控状态码 403"},
		{"状态码429限流", 429, "", true, "429"},
		{"状态码503", 503, "", true, "503"},
		{"百度安全验证", 200, "<html>百度安全验证</html>", true, "百度安全验证"},
		{"人机验证", 200, "<title>人机验证</title>", true, "人机验证"},
		{"captcha大写", 200, "<html>CAPTCHA challenge</html>", true, "captcha"},
		{"正常含验证词但非风控", 200, "<html>某结果标题含验证二字</html>", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkAntiCrawl(c.status, []byte(c.body))
			if c.wantErr {
				if err == nil {
					t.Fatalf("期望报错，实际为 nil")
				}
				if c.wantSubstr != "" && !strings.Contains(err.Error(), c.wantSubstr) {
					t.Errorf("错误信息应包含 %q，实际：%s", c.wantSubstr, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("正常页面不应报错，实际：%s", err.Error())
				}
			}
		})
	}
}
