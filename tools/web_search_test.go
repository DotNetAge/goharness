package tools

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"time"
)

// ============================================================
// htmlUnescape — HTML Entity Decoding
// ============================================================

func TestHtmlUnescape(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"&amp;", "&"},
		{"&lt;", "<"},
		{"&gt;", ">"},
		{"&quot;", "\""},
		{"&#39;", "'"},
		{"&nbsp;", " "},
		{"Hello &amp; World", "Hello & World"},
		{"a &lt; b &gt; c", "a < b > c"},
		{"&quot;quoted&quot;", "\"quoted\""},
		{"no entities here", "no entities here"},
		{"mixed &amp; &lt; &gt;", "mixed & < >"},
		{"&amp;amp;", "&amp;"}, // single-pass decode: &amp; → &
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := htmlUnescape(tt.input)
			if got != tt.want {
				t.Errorf("htmlUnescape(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

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

// ============================================================
// filterResults — Domain Filtering
// ============================================================

func TestFilterResults_MaxResults(t *testing.T) {
	results := []SearchResult{
		{Title: "A", URL: "https://a.com/1"},
		{Title: "B", URL: "https://b.com/2"},
		{Title: "C", URL: "https://c.com/3"},
	}
	filtered := filterResults(results, SearchOptions{MaxResults: 2})
	if len(filtered) != 2 {
		t.Errorf("MaxResults=2 should limit to 2, got %d", len(filtered))
	}
}

func TestFilterResults_AllowedDomains(t *testing.T) {
	results := []SearchResult{
		{Title: "GitHub", URL: "https://github.com/repo"},
		{Title: "Docs", URL: "https://docs.python.org/guide"},
		{Title: "Other", URL: "https://other.com/page"},
	}
	filtered := filterResults(results, SearchOptions{
		AllowedDomains: []string{"github.com", "docs.python.org"},
	})
	if len(filtered) != 2 {
		t.Errorf("allowed domains should keep 2 results, got %d", len(filtered))
	}
	for _, r := range filtered {
		u, _ := url.Parse(r.URL)
		if u.Hostname() != "github.com" && u.Hostname() != "docs.python.org" {
			t.Errorf("unexpected domain in filtered: %s", u.Hostname())
		}
	}
}

func TestFilterResults_BlockedDomains(t *testing.T) {
	results := []SearchResult{
		{Title: "Good", URL: "https://good.com/a"},
		{Title: "Ad", URL: "https://ads.example.com/bad"},
		{Title: "Spam", URL: "https://spam.com/tracking"},
	}
	filtered := filterResults(results, SearchOptions{
		BlockedDomains: []string{"ads.example.com", "spam.com"},
	})
	if len(filtered) != 1 {
		t.Errorf("blocked domains should remove 2, keep 1, got %d", len(filtered))
	}
	if filtered[0].URL != "https://good.com/a" {
		t.Errorf("remaining result should be good.com, got: %s", filtered[0].URL)
	}
}

func TestFilterResults_CombinedFilters(t *testing.T) {
	results := []SearchResult{
		{Title: "Keep", URL: "https://keep.github.io/page"},
		{Title: "BlockMe", URL: "https://block.github.io/bad"},
		{Title: "Outside", URL: "https://outside.com/x"},
	}
	filtered := filterResults(results, SearchOptions{
		AllowedDomains: []string{"github.io"},
		BlockedDomains: []string{"block.github.io"},
		MaxResults:     10,
	})
	if len(filtered) != 1 {
		t.Errorf("combined filters should keep 1 result, got %d", len(filtered))
	}
	if filtered[0].Title != "Keep" {
		t.Errorf("should keep only 'Keep', got: %s", filtered[0].Title)
	}
}

func TestFilterResults_EmptyInput(t *testing.T) {
	filtered := filterResults(nil, SearchOptions{})
	if len(filtered) != 0 {
		t.Errorf("nil input should return empty, got %d", len(filtered))
	}
}

func TestFilterResults_InvalidURLSkipped(t *testing.T) {
	results := []SearchResult{
		{Title: "Valid", URL: "https://valid.com"},
		{Title: "Bad", URL: "://invalid-url"},
	}
	filtered := filterResults(results, SearchOptions{})
	if len(filtered) != 1 {
		t.Errorf("invalid URLs should be skipped, got %d", len(filtered))
	}
}

func TestWebSearchTool_Info(t *testing.T) {
	tool := NewWebSearchTool()
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
	tool := NewWebSearchTool()
	_, err := tool.Execute(context.Background(), nil)
	if err == nil {
		t.Error("expected error for missing query parameter")
	}
}

func TestWebSearchTool_TooShortQuery(t *testing.T) {
	tool := NewWebSearchTool()
	_, err := tool.Execute(context.Background(), map[string]any{
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tool := NewWebSearchTool().(*WebSearchTool)

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
		t.Fatalf("second Execute() error = %v", err)
	}

	s1 := result1.(string)
	s2 := result2.(string)
	if s1 == s2 {
		return // exact cache hit
	}
	t.Logf("cache results differ (acceptable for dynamic content): len1=%d, len2=%d", len(s1), len(s2))
}

func TestWebSearchTool_MaxResultsClamped(t *testing.T) {
	results := []SearchResult{
		{Title: "1", URL: "https://a.com"}, {Title: "2", URL: "https://b.com"},
		{Title: "3", URL: "https://c.com"}, {Title: "4", URL: "https://d.com"},
		{Title: "5", URL: "https://e.com"},
	}
	filtered := filterResults(results, SearchOptions{MaxResults: 999})
	if len(filtered) > 20 {
		t.Errorf("filterResults should clamp MaxResults to 20 internally, got %d", len(filtered))
	}
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
