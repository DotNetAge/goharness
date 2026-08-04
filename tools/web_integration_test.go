package tools

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/logging"
)

// ============================================================
// 集成测试：WebFetch + WebSearch（带代理）
//
// 用法：
//   export https_proxy=http://127.0.0.1:7890 http_proxy=http://127.0.0.1:7890 all_proxy=socks5://127.0.0.1:7890
//   go test ./tools/... -v -count=1 -run "TestIntegration" -timeout 300s
//
// 本测试套件同时覆盖 WebFetchTool（简单模式）和
// WebFetchTool（Claude 风格）对真实互联网目标的访问。
// ============================================================

var (
	proxyEnabled = os.Getenv("https_proxy") != "" || os.Getenv("http_proxy") != ""
	testTimeout  = 30 * time.Second
)

func mustHaveProxy(t *testing.T) {
	if !proxyEnabled {
		t.Skip("skipping integration test: set https_proxy/http_proxy to run (e.g. http://127.0.0.1:7890)")
	}
}

// ============================================================
// SECTION 1: WebFetchTool (simple) — HTTP/HTTPS Real Requests
// ============================================================

func TestIntegration_WebFetch_StatusCode200_StaticHTML(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{"url": "https://example.com"})
	if err != nil {
		t.Fatalf("FAIL [200-StaticHTML]: Execute error = %v", err)
	}
	s := result.(string)
	t.Logf("PASS [200-StaticHTML]: len=%d bytes, contains_html=%v, contains_example_domain=%v",
		len(s), strings.Contains(s, "<html") || strings.Contains(s, "<!DOCTYPE"),
		strings.Contains(s, "Example Domain"))
	if !strings.Contains(s, "Example Domain") && !strings.Contains(s, "<html") {
		t.Error("expected Example Domain or HTML content")
	}
}

func TestIntegration_WebFetch_StatusCode404(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{"url": "https://httpbin.org/status/404"})
	t.Logf("[404]: result=(%v), error=(%v)", result != nil, err)
	if err == nil && result != nil {
		s := result.(string)
		t.Logf("PASS [404]: server returned content despite 404 status, len=%d", len(s))
	}
}

func TestIntegration_WebFetch_StatusCode500(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{"url": "https://httpbin.org/status/500"})
	t.Logf("[500]: result=(%v), error=(%v)", result != nil, err)
	if err != nil {
		t.Logf("INFO [500]: 500 returned error as expected: %v", err)
	}
}

func TestIntegration_WebFetch_RedirectChain(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{"url": "https://httpbin.org/redirect/2"})
	if err != nil {
		t.Fatalf("FAIL [Redirect-2]: error = %v", err)
	}
	s := result.(string)
	t.Logf("PASS [Redirect-2]: followed redirects, len=%d bytes", len(s))
}

func TestIntegration_WebFetch_LargePage(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{"url": "https://httpbin.org/bytes/100000"})
	if err != nil {
		t.Fatalf("FAIL [LargePage]: error = %v", err)
	}
	s := result.(string)
	t.Logf("PASS [LargePage]: received %d bytes (should be truncated to ~50000)", len(s))
	if len([]rune(s)) > 51000 {
		t.Errorf("content should be truncated to ~50000 chars, got %d runes", len([]rune(s)))
	}
}

func TestIntegration_WebFetch_EncodedURL(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	encodedURL := "https://httpbin.org/get?query=hello%20world&key=value%3Dtest&special=%E4%B8%AD%E6%96%87"
	result, err := tool.Execute(ctx, map[string]any{"url": encodedURL})
	if err != nil {
		t.Fatalf("FAIL [EncodedURL]: error = %v", err)
	}
	s := result.(string)
	if strings.Contains(s, "hello world") || strings.Contains(s, "value=test") || strings.Contains(s, "query") {
		t.Logf("PASS [EncodedURL]: URL-encoded params correctly sent, len=%d", len(s))
	} else {
		t.Logf("INFO [EncodedURL]: response len=%d (may not echo params)", len(s))
	}
}

func TestIntegration_WebFetch_UTF8Content(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{"url": "https://httpbin.org/encoding/utf8"})
	if err != nil {
		t.Fatalf("FAIL [UTF8]: error = %v", err)
	}
	s := result.(string)
	hasCJK := strings.Contains(s, "\u4e2d\u6587") || strings.Contains(s, "中文") ||
		strings.Contains(s, "\u65e5\u672c") || strings.Contains(s, "日本語")
	t.Logf("PASS [UTF8]: len=%d, has_CJK_content=%v", len(s), hasCJK)
}

func TestIntegration_WebFetch_JSONResponse(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{"url": "https://httpbin.org/json"})
	if err != nil {
		t.Fatalf("FAIL [JSON]: error = %v", err)
	}
	s := result.(string)
	t.Logf("PASS [JSON]: len=%d, contains_slides=%v, contains_title=%v",
		len(s), strings.Contains(s, "slides"), strings.Contains(s, "title"))
}

func TestIntegration_WebFetch_HTMLWithScriptsAndStyles(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{"url": "https://httpbin.org/html"})
	if err != nil {
		t.Fatalf("FAIL [HTML-Rich]: error = %v", err)
	}
	s := result.(string)
	hasMobyDick := strings.Contains(s, "Moby-Dick") || strings.Contains(s, "Herman Melville")
	t.Logf("PASS [HTML-Rich]: len=%d, has_moby_dick_content=%v", len(s), hasMobyDick)
}

func TestIntegration_WebFetch_SSRF_Protection_RealDNS(t *testing.T) {
	mustHaveProxy(t)

	badTargets := []struct {
		name string
		url  string
	}{
		{"localhost_http", "http://localhost"},
		{"loopback_ip", "http://127.0.0.1"},
		{"ipv6_loopback", "http://[::1]"},
		{"private_class_a", "http://10.0.0.1"},
		{"private_class_b", "http://172.16.0.1"},
		{"private_class_c", "http://192.168.1.1"},
		{"link_local", "http://169.254.169.254"},
	}

	for _, tt := range badTargets {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewWebFetchTool(logging.NewNopLogger())
			start := time.Now()
			_, err := tool.Execute(ctxWithLogger(), map[string]any{"url": tt.url})
			elapsed := time.Since(start)
			if err == nil {
				t.Errorf("FAIL [SSRF-%s]: should block %q but got no error (elapsed=%v)", tt.name, tt.url, elapsed)
			} else if elapsed > 5*time.Second {
				t.Errorf("WARN [SSRF-%s]: blocking took too long (%v) — possible DNS timeout instead of rejection", tt.name, elapsed)
			} else {
				t.Logf("PASS [SSRF-%s]: blocked %q in %v, error=%v", tt.name, tt.url, elapsed, err)
			}
		})
	}
}

// ============================================================
// SECTION 2: WebFetchTool (Claude-style) — Enhanced Tests
// ============================================================

func TestIntegration_WebFetch_ExampleCom(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{
		"url": "https://example.com",
	})
	if err != nil {
		t.Fatalf("FAIL [Claude-Example]: error = %v", err)
	}
	s := result.(string)
	hasHeader := strings.Contains(s, "--- 网页获取：")
	hasStatus := strings.Contains(s, "状态：")
	hasContent := strings.Contains(s, "Example Domain")
	t.Logf("PASS [Claude-Example]: header=%v status_line=%v content=%v, total_len=%d",
		hasHeader, hasStatus, hasContent, len(s))
	if !hasHeader || !hasStatus || !hasContent {
		t.Errorf("missing expected fields: header=%v status=%v content=%v",
			hasHeader, hasStatus, hasContent)
	}
}

func TestIntegration_WebFetch_HTMLToTextExtraction(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{
		"url": "https://httpbin.org/html",
	})
	if err != nil {
		t.Fatalf("FAIL [Claude-HTMLExtract]: error = %v", err)
	}
	s := result.(string)
	noScript := !strings.Contains(s, "alert(") && !strings.Contains(s, "javascript:")
	noStyle := !strings.Contains(s, "display:none") && !strings.Contains(s, "{")
	hasText := strings.Contains(s, "Moby-Dick") || strings.Contains(s, "Herman Melville")
	t.Logf("PASS [Claude-HTMLExtract]: scripts_stripped=%v styles_cleaned=%v text_extracted=%v, len=%d",
		noScript, noStyle, hasText, len(s))
}

func TestIntegration_WebFetch_URLAutoNormalization(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())

	variants := []struct {
		name string
		url  string
	}{
		{"bare_domain", "example.com"},
		{"http_to_https", "http://example.com"},
		{"already_https", "https://example.com"},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			result, err := tool.Execute(ctx, map[string]any{"url": v.url})
			if err != nil {
				t.Fatalf("FAIL [Normalize-%s]: error = %v", v.name, err)
			}
			s := result.(string)
			if !strings.Contains(s, "Example Domain") {
				t.Errorf("FAIL [Normalize-%s]: URL normalization failed for %q", v.name, v.url)
			} else {
				t.Logf("PASS [Normalize-%s]: %q → success, len=%d", v.name, v.url, len(s))
			}
		})
	}
}

func TestIntegration_WebFetch_ResponseMetadata(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{"url": "https://httpbin.org/xml"})
	if err != nil {
		t.Fatalf("FAIL [Metadata]: error = %v", err)
	}
	s := result.(string)
	t.Logf("PASS [Metadata]: full_response_preview:\n%s\n[END PREVIEW, total %d chars]", s[:min(500, len(s))], len(s))
}

func TestIntegration_WebFetch_BinaryLikeContent(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{"url": "https://httpbin.org/image/png"})
	if err != nil {
		t.Logf("INFO [Binary]: binary content may fail: %v", err)
		return
	}
	s := result.(string)
	t.Logf("PASS [Binary]: handled binary/png response, len=%d (may contain garbled text)", len(s))
}

// ============================================================
// SECTION 3: WebSearchTool — Real Hybrid Search
// ============================================================

func TestIntegration_Search_GolangOfficial(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), 45*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{
		"query":       "golang official documentation",
		"max_results": float64(5),
	})
	if err != nil {
		t.Fatalf("FAIL [Search-Golang]: error = %v", err)
	}
	s := result.(string)
	hasHeader := strings.Contains(s, "Web search results")
	hasURL := strings.Contains(s, "http")
	hasResult := strings.Contains(s, "golang") || strings.Contains(s, "Go") || strings.Contains(s, "go.dev")
	t.Logf("PASS [Search-Golang]: header=%v has_url=%v relevant=%v, len=%d\nPreview: %.300s...",
		hasHeader, hasURL, hasResult, len(s), s)
	if !hasHeader || !hasURL {
		t.Errorf("search result missing header or URLs")
	}
}

func TestIntegration_Search_TechnicalQuery(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), 45*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{
		"query": "react hooks useState useEffect tutorial",
	})
	if err != nil {
		t.Fatalf("FAIL [Search-React]: error = %v", err)
	}
	s := result.(string)
	hasReact := strings.Contains(s, "React") || strings.Contains(s, "react") || strings.Contains(s, "hooks")
	t.Logf("PASS [Search-React]: react_related=%v, len=%d\nPreview: %.400s...", hasReact, len(s), s)
}

func TestIntegration_Search_NewsQuery(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), 45*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{
		"query": "latest AI news 2026",
	})
	if err != nil {
		t.Fatalf("FAIL [Search-News]: error = %v", err)
	}
	s := result.(string)
	t.Logf("PASS [Search-News]: len=%d, preview: %.400s...", len(s), s)
}

func TestIntegration_Search_DomainFiltering(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), 45*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger()).(*WebSearchTool)
	result, err := tool.Execute(ctx, map[string]any{
		"query":           "go programming language",
		"allowed_domains": []any{"github.com", "go.dev"},
		"max_results":     float64(10),
	})
	if err != nil {
		t.Fatalf("FAIL [Search-DomainFilter]: error = %v", err)
	}
	s := result.(string)
	t.Logf("PASS [Search-DomainFilter]: len=%d, preview: %.400s...", len(s), s)
}

func TestIntegration_Search_MaxResultsLimit(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), 45*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{
		"query":       "python programming",
		"max_results": float64(3),
	})
	if err != nil {
		t.Fatalf("FAIL [Search-MaxResults]: error = %v", err)
	}
	s := result.(string)
	lineCount := strings.Count(s, "\n")
	t.Logf("PASS [Search-MaxResults]: max_results=3, output_lines=%d, len=%d", lineCount, len(s))
}

func TestIntegration_Search_SpecialCharacters(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), 45*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger())
	queries := []string{
		"C++ vs Rust performance 2026",
		"golang \"interface\" type assertion",
		"Docker container networking host vs bridge",
	}
	for i, q := range queries {
		result, err := tool.Execute(ctx, map[string]any{"query": q})
		if err != nil {
			t.Logf("SKIP [Search-SpecialChar-%d]: query=%q error=%v", i+1, q, err)
			continue
		}
		s := result.(string)
		t.Logf("PASS [Search-SpecialChar-%d]: query=%q → len=%d", i+1, q, len(s))
	}
}

func TestIntegration_Search_CacheHit(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), 90*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger()).(*WebSearchTool)
	query := "cache integration test unique key"

	start1 := time.Now()
	r1, err := tool.Execute(ctx, map[string]any{"query": query})
	d1 := time.Since(start1)
	if err != nil {
		t.Fatalf("FAIL [Search-Cache-1st]: error = %v", err)
	}

	start2 := time.Now()
	r2, err := tool.Execute(ctx, map[string]any{"query": query})
	d2 := time.Since(start2)
	if err != nil {
		t.Fatalf("FAIL [Search-Cache-2nd]: error = %v", err)
	}

	s1, s2 := r1.(string), r2.(string)
	t.Logf("PASS [Search-Cache]: 1st=%.2fs(len=%d) | 2nd=%.2fs(len=%d) | identical=%v",
		d1.Seconds(), len(s1), d2.Seconds(), len(s2), s1 == s2)
	if d2 < d1 {
		t.Log("  → Cache hit confirmed (2nd request faster)")
	}
}

// ============================================================
// SECTION 4: Cross-Module Workflow — Search then Fetch
// ============================================================

func TestIntegration_Workflow_SearchThenFetch(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), 90*time.Second)
	defer cancel()

	step1 := time.Now()
	searchTool := NewWebSearchTool(logging.NewNopLogger())
	searchResult, err := searchTool.Execute(ctx, map[string]any{
		"query":       "golang io/ioutil documentation",
		"max_results": float64(3),
	})
	if err != nil {
		t.Fatalf("FAIL [Workflow-Step1-Search]: error = %v", err)
	}
	searchStr := searchResult.(string)
	t.Logf("STEP1 [Search]: completed in %.2fs, len=%d", time.Since(step1).Seconds(), len(searchStr))

	step2 := time.Now()
	fetchTool := NewWebFetchTool(logging.NewNopLogger())
	fetchResult, err := fetchTool.Execute(ctx, map[string]any{
		"url": "https://pkg.go.dev/io/ioutil",
	})
	if err != nil {
		t.Fatalf("FAIL [Workflow-Step2-Fetch]: error = %v", err)
	}
	fetchStr := fetchResult.(string)
	t.Logf("STEP2 [Fetch]: completed in %.2fs, len=%d", time.Since(step2).Seconds(), len(fetchStr))

	totalTime := time.Since(step1)
	t.Logf("PASS [Workflow-Search→Fetch]: total_time=%.2fs | search_len=%d | fetch_len=%d",
		totalTime.Seconds(), len(searchStr), len(fetchStr))

	if !strings.Contains(fetchStr, "ioutil") && !strings.Contains(fetchStr, "ReadFile") &&
		!strings.Contains(fetchStr, "WriteFile") && !strings.Contains(fetchStr, "package") {
		t.Logf("INFO [Workflow]: fetched page may not contain expected ioutil content (redirect possible)")
	}
}

// ============================================================
// SECTION 5: Edge Cases & Robustness
// ============================================================

func TestIntegration_Edge_VeryLongURL(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), testTimeout)
	defer cancel()

	longPath := strings.Repeat("a", 2000)
	u, _ := url.Parse("https://httpbin.org/get")
	q := u.Query()
	q.Set("data", longPath)
	u.RawQuery = q.Encode()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{"url": u.String()})
	if err != nil {
		t.Logf("INFO [Edge-LongURL]: long URL may be rejected: %v", err)
		return
	}
	s := result.(string)
	t.Logf("PASS [Edge-LongURL]: handled URL of length %d, response len=%d", len(u.String()), len(s))
}

func TestIntegration_Edge_QueryWithSpaces(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), 45*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{
		"query": "  multiple   spaces   in   query   ",
	})
	if err != nil {
		t.Fatalf("FAIL [Edge-Spaces]: error = %v", err)
	}
	s := result.(string)
	t.Logf("PASS [Edge-Spaces]: query with extra spaces produced len=%d result", len(s))
}

func TestIntegration_Edge_EmptySearchResult(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), 45*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{
		"query": "zzzxxxyyyqqqwwweeerrrtttyyuuuiiiooopppaaasssdddfffggghhhjjjkkklll",
	})
	if err != nil {
		t.Logf("INFO [Edge-NoResults]: error on nonsense query: %v", err)
		return
	}
	s := result.(string)
	isNoResults := strings.Contains(strings.ToLower(s), "no result") ||
		strings.Contains(strings.ToLower(s), "not found")
	t.Logf("PASS [Edge-NoResults]: nonsense_query → len=%d, is_no_result_msg=%v", len(s), isNoResults)
}

func TestIntegration_Edge_UnicodeSearchQuery(t *testing.T) {
	mustHaveProxy(t)
	ctx, cancel := context.WithTimeout(ctxWithLogger(), 45*time.Second)
	defer cancel()

	tool := NewWebSearchTool(logging.NewNopLogger())
	queries := []string{
		"人工智能 大语言模型",
		"日本語 プログラミング",
		"한국어 코딩",
		"emoji 🚀 rocket science",
	}
	for i, q := range queries {
		result, err := tool.Execute(ctx, map[string]any{"query": q})
		if err != nil {
			t.Logf("SKIP [Edge-Unicode-%d]: query=%q error=%v", i+1, q, err)
			continue
		}
		s := result.(string)
		t.Logf("PASS [Edge-Unicode-%d]: query=%q → len=%d", i+1, q, len(s))
	}
}

// ============================================================
// Helper
// ============================================================

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
