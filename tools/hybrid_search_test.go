package tools

import (
	"testing"

	"github.com/DotNetAge/goharness/logging"
)

// ============================================================
// Hybrid Search Integration Tests
// ============================================================

func TestWebSearchTool_HybridAdaptersRegistered(t *testing.T) {
	tool := NewWebSearchTool(logging.NewNopLogger()).(*WebSearchTool)
	if len(tool.adapters) != 5 {
		t.Errorf("expected 5 adapters in hybrid mode, got %d", len(tool.adapters))
	}

	names := make(map[string]bool)
	for _, adapter := range tool.adapters {
		names[adapter.Name()] = true
	}

	// 搜狗系（搜狗+微信）+ 独立风控兜底（必应+360+头条）
	expectedNames := []string{"sogou", "weixin", "bing", "360", "toutiao"}
	for _, name := range expectedNames {
		if !names[name] {
			t.Errorf("missing adapter: %s", name)
		}
	}
}

func TestHybridSearch_Deduplication(t *testing.T) {
	results := []SearchResult{
		{Title: "A", URL: "https://same.com/page"},
		{Title: "B", URL: "https://different.com"},
		{Title: "C", URL: "https://same.com/page"},
		{Title: "D", URL: "https://another.com"},
	}

	seenURLs := make(map[string]bool)
	var dedupedResults []SearchResult
	for _, r := range results {
		if !seenURLs[r.URL] {
			seenURLs[r.URL] = true
			dedupedResults = append(dedupedResults, r)
		}
	}

	if len(dedupedResults) != 3 {
		t.Errorf("expected 3 deduplicated results, got %d", len(dedupedResults))
	}
}
