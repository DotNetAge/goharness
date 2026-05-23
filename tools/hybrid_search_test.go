package tools

import (
	"context"
	"testing"
)

// ============================================================
// HaosouAdapter (360 Search) Unit Tests
// ============================================================

func TestHaosouAdapter_Name(t *testing.T) {
	a := NewHaosouAdapter()
	if a.Name() != "haosou" {
		t.Errorf("Name() = %q, want %q", a.Name(), "haosou")
	}
}

func TestHaosouAdapter_Search_ContextCancelled(t *testing.T) {
	a := NewHaosouAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := a.Search(ctx, "test query", SearchOptions{})
	if err == nil {
		t.Error("expected error when context is already cancelled")
	}
}

func TestParseHaosouHTML_SingleResult(t *testing.T) {
	htmlBody := []byte(`<html><body>
<div class="res-list" data-res="1">
<h3 class="res-title"><a href="https://example.com">Example Page</a></h3>
<p class="res-desc">This is a snippet about example page.</p>
</div>
</body></html>`)

	results := parseHaosouHTML(htmlBody)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Title != "Example Page" {
		t.Errorf("Title = %q, want %q", results[0].Title, "Example Page")
	}
	if results[0].URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", results[0].URL, "https://example.com")
	}
}

func TestParseHaosouHTML_MultipleResults(t *testing.T) {
	htmlBody := []byte(`<html><body>
<div class="res-list" data-res="1">
<h3 class="res-title"><a href="https://site1.com">Site One</a></h3>
<p class="res-desc">First result snippet.</p>
</div>
<div class="res-list" data-res="2">
<h3 class="res-title"><a href="https://site2.com">Site Two</a></h3>
<p class="res-desc">Second result with more detail.</p>
</div>
</body></html>`)

	results := parseHaosouHTML(htmlBody)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestParseHaosouHTML_NoResults(t *testing.T) {
	htmlBody := []byte(`<html><body><h1>No Results Found</h1></body></html>`)
	results := parseHaosouHTML(htmlBody)
	if len(results) != 0 {
		t.Errorf("expected 0 results for no-result HTML, got %d", len(results))
	}
}

// ============================================================
// SogouAdapter Unit Tests
// ============================================================

func TestSogouAdapter_Name(t *testing.T) {
	a := NewSogouAdapter()
	if a.Name() != "sogou" {
		t.Errorf("Name() = %q, want %q", a.Name(), "sogou")
	}
}

func TestSogouAdapter_Search_ContextCancelled(t *testing.T) {
	a := NewSogouAdapter()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := a.Search(ctx, "test query", SearchOptions{})
	if err == nil {
		t.Error("expected error when context is already cancelled")
	}
}

func TestParseSogouHTML_SingleResult(t *testing.T) {
	htmlBody := []byte(`<html><body>
<div class="vrwrap">
<h3 class="vr-title"><a href="https://example.com">Example Page</a></h3>
<p class="str-text-info">This is a snippet about example page.</p>
</div>
</body></html>`)

	results := parseSogouHTML(htmlBody)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Title != "Example Page" {
		t.Errorf("Title = %q, want %q", results[0].Title, "Example Page")
	}
	if results[0].URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", results[0].URL, "https://example.com")
	}
}

func TestParseSogouHTML_MultipleResults(t *testing.T) {
	htmlBody := []byte(`<html><body>
<div class="vrwrap">
<h3 class="vr-title"><a href="https://site1.com">Site One</a></h3>
<p class="str-text-info">First result snippet.</p>
</div>
<div class="vrwrap">
<h3 class="vr-title"><a href="https://site2.com">Site Two</a></h3>
<p class="str-text-info">Second result with more detail.</p>
</div>
</body></html>`)

	results := parseSogouHTML(htmlBody)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
}

func TestParseSogouHTML_NoResults(t *testing.T) {
	htmlBody := []byte(`<html><body><h1>No Results Found</h1></body></html>`)
	results := parseSogouHTML(htmlBody)
	if len(results) != 0 {
		t.Errorf("expected 0 results for no-result HTML, got %d", len(results))
	}
}

// ============================================================
// Hybrid Search Integration Tests
// ============================================================

func TestWebSearchTool_HybridAdaptersRegistered(t *testing.T) {
	tool := NewWebSearchTool().(*WebSearchTool)
	if len(tool.adapters) != 4 {
		t.Errorf("expected 4 adapters in hybrid mode, got %d", len(tool.adapters))
	}

	names := make(map[string]bool)
	for _, adapter := range tool.adapters {
		names[adapter.Name()] = true
	}

	expectedNames := []string{"baidu", "haosou", "sogou"}
	for _, name := range expectedNames {
		if !names[name] {
			t.Errorf("missing adapter: %s", name)
		}
	}
}

func TestHybridSearch_Deduplication(t *testing.T) {
	results := []SearchResult{
		{Title: "A", URL: "https://same.com/1"},
		{Title: "B", URL: "https://different.com"},
		{Title: "C", URL: "https://same.com/2"},
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
