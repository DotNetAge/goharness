package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestHybridSearch_RealKeyword tests the hybrid search with a real keyword
func TestHybridSearch_RealKeyword(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tool := NewWebSearchTool()
	result, err := tool.Execute(ctx, map[string]any{
		"query":       "Agentic应用2026的趋势",
		"max_results": float64(15),
	})
	if err != nil {
		t.Fatalf("hybrid search failed: %v", err)
	}

	s := result.(string)
	t.Logf("=== 混合搜索结果 ===\n%s\n", s)

	if s == "" {
		t.Fatal("expected non-empty search result")
	}
	if !strings.Contains(s, "搜索结果") {
		t.Errorf("result should contain header, got: %.200s...", s)
	}
	if !strings.Contains(s, "http") {
		t.Error("result should contain at least one URL")
	}

	resultLines := strings.Split(s, "\n")
	urlCount := 0
	for _, line := range resultLines {
		if strings.Contains(line, "- **URL**:") {
			urlCount++
		}
	}
	t.Logf("找到 %d 个URL链接", urlCount)
	if urlCount < 1 {
		t.Errorf("expected at least 1 URL from hybrid search, got %d", urlCount)
	}
}

// TestHybridSearch_AdapterPerformance tests which adapters succeed
func TestHybridSearch_AdapterPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	adapters := []SearchAdapter{
		NewBaiduAdapter(),
		NewSogouAdapter(),
		NewWeixinAdapter(),
	}

	query := "Agentic RAG 应该如何设计"
	opts := SearchOptions{MaxResults: 5}

	type adapterResult struct {
		name    string
		results []SearchResult
		err     error
	}

	var results []adapterResult
	for _, adapter := range adapters {
		start := time.Now()
		searchResults, err := adapter.Search(ctx, query, opts)
		elapsed := time.Since(start)

		result := adapterResult{
			name:    adapter.Name(),
			results: searchResults,
			err:     err,
		}

		if err != nil {
			t.Logf("❌ %s 失败 (%v): %v (耗时: %v)", adapter.Name(), elapsed, err, elapsed)
		} else if len(searchResults) == 0 {
			t.Logf("⚠️  %s 返回空结果 (耗时: %v)", adapter.Name(), elapsed)
		} else {
			t.Logf("✅ %s 成功: %d 条结果 (耗时: %v)", adapter.Name(), len(searchResults), elapsed)
		}

		results = append(results, result)
	}

	successCount := 0
	for _, r := range results {
		if r.err == nil && len(r.results) > 0 {
			successCount++
		}
	}
	t.Logf("\n=== 搜索引擎成功率 ===\n总引擎数: %d\n成功引擎数: %d\n成功率: %.1f%%",
		len(adapters), successCount, float64(successCount)/float64(len(adapters))*100)

	if successCount == 0 {
		t.Error("所有搜索引擎都失败了（可能被GFW拦截或网络问题）")
	}
}
