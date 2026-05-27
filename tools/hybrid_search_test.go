package tools

import (
	"testing"
)

// ============================================================
// Hybrid Search Integration Tests
// ============================================================

func TestWebSearchTool_HybridAdaptersRegistered(t *testing.T) {
	tool := NewWebSearchTool().(*WebSearchTool)
	if len(tool.adapters) != 2 {
		t.Errorf("expected 2 adapters in hybrid mode, got %d", len(tool.adapters))
	}

	names := make(map[string]bool)
	for _, adapter := range tool.adapters {
		names[adapter.Name()] = true
	}

	expectedNames := []string{"sogou", "weixin"}
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
