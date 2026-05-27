package tools

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DotNetAge/goreact/memory"
)

// mockMemory implements memory.Memory for testing
type mockMemory struct {
	records []memory.MemoryRecord
	err     error
}

func (m *mockMemory) Retrieve(_ context.Context, query string, _ ...memory.RetrieveOption) ([]memory.MemoryRecord, error) {
	if m.err != nil {
		return nil, m.err
	}
	if query == "error" {
		return nil, errors.New("memory retrieval failed")
	}

	var results []memory.MemoryRecord
	for _, r := range m.records {
		results = append(results, r)
	}
	return results, nil
}

func (m *mockMemory) Store(_ context.Context, record memory.MemoryRecord) (string, error) {
	return "test-id", nil
}

func (m *mockMemory) Delete(_ context.Context, id string) error {
	return nil
}

func TestNewMemorySearch_NilMemory(t *testing.T) {
	tool := NewMemorySearch(nil)
	if tool != nil {
		t.Error("expected nil when memory is nil")
	}
}

func TestNewMemorySearch_ValidMemory(t *testing.T) {
	mem := &mockMemory{}
	tool := NewMemorySearch(mem)
	if tool == nil {
		t.Fatal("expected non-nil tool when memory is provided")
	}

	info := tool.Info()
	if info.Name != "MemorySearch" {
		t.Errorf("expected tool name 'MemorySearch', got %q", info.Name)
	}
}

func TestMemorySearch_Info(t *testing.T) {
	mem := &mockMemory{}
	tool := NewMemorySearch(mem).(*MemorySearch)

	info := tool.Info()
	if info == nil {
		t.Fatal("Info() should not return nil")
	}

	if info.Name != "MemorySearch" {
		t.Errorf("expected name 'MemorySearch', got %q", info.Name)
	}
	if info.Description == "" {
		t.Error("Description should not be empty")
	}
	if !info.IsReadOnly {
		t.Error("MemorySearch should be read-only")
	}

	requiredParams := 0
	for _, p := range info.Parameters {
		if p.Required {
			requiredParams++
		}
		if p.Name == "query" && !p.Required {
			t.Error("query parameter should be required")
		}
	}
	if requiredParams != 1 {
		t.Errorf("expected 1 required parameter, got %d", requiredParams)
	}
}

func TestMemorySearch_Execute_Success(t *testing.T) {
	mem := &mockMemory{
		records: []memory.MemoryRecord{
			{
				ID:        "mem-1",
				Type:      memory.MemoryTypeLongTerm,
				Title:     "User Prefers TypeScript",
				Content:   "User explicitly prefers TypeScript over JavaScript for all new projects",
				Tags:      []string{"preference", "typescript"},
				Score:     0.95,
				CreatedAt: time.Now(),
			},
			{
				ID:        "mem-2",
				Type:      memory.MemoryTypeSession,
				Title:     "Current Project Structure",
				Content:   "Project uses monorepo structure with packages in /packages directory",
				Tags:      []string{"project", "structure"},
				Score:     0.85,
				CreatedAt: time.Now(),
			},
		},
	}

	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{
		"query": "user preferences for programming languages",
		"limit": float64(10),
	}

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	resultStr, ok := result.(string)
	if !ok {
		t.Fatal("result should be a string")
	}

	if !contains(resultStr, "TypeScript") {
		t.Error("result should contain information about TypeScript preference")
	}
	if !contains(resultStr, "2 relevant memory record") {
		t.Error("result should indicate 2 records found")
	}
	if !contains(resultStr, "Long-term Knowledge") {
		t.Error("result should contain memory type label")
	}
	if !contains(resultStr, "Relevance:") {
		t.Error("result should show relevance score")
	}
}

func TestMemorySearch_Execute_EmptyResult(t *testing.T) {
	mem := &mockMemory{
		records: []memory.MemoryRecord{},
	}

	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{
		"query": "nonexistent topic that has no memories",
	}

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	resultStr := result.(string)
	if !contains(resultStr, "No memories found") {
		t.Errorf("empty result should indicate no memories found, got: %s", resultStr)
	}
}

func TestMemorySearch_Execute_Error(t *testing.T) {
	mem := &mockMemory{err: errors.New("memory system unavailable")}

	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{
		"query": "test query",
	}

	_, err := tool.Execute(context.Background(), params)
	if err == nil {
		t.Fatal("Execute() should return error when memory fails")
	}

	if !contains(err.Error(), "memory search failed") {
		t.Errorf("error should mention memory search failure, got: %v", err)
	}
}

func TestMemorySearch_Execute_MissingQuery(t *testing.T) {
	mem := &mockMemory{}
	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{}

	_, err := tool.Execute(context.Background(), params)
	if err == nil {
		t.Fatal("Execute() should return error when query is missing")
	}
}

func TestMemorySearch_Execute_ShortQuery(t *testing.T) {
	mem := &mockMemory{}
	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{
		"query": "x",
	}

	_, err := tool.Execute(context.Background(), params)
	if err == nil {
		t.Fatal("Execute() should return error for short query")
	}
}

func TestMemorySearch_Execute_WithTypesFilter(t *testing.T) {
	mem := &mockMemory{
		records: []memory.MemoryRecord{
			{
				ID:      "mem-1",
				Type:    memory.MemoryTypeLongTerm,
				Title:   "Long Term Memory",
				Content: "This is long-term knowledge",
			},
		},
	}

	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{
		"query": "memory test",
		"types": []any{"longterm"},
	}

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if result == nil {
		t.Fatal("result should not be nil")
	}
}

func TestMemorySearch_LimitValidation(t *testing.T) {
	mem := &mockMemory{
		records: make([]memory.MemoryRecord, 25),
	}

	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{
		"query": "test",
		"limit": float64(100),
	}

	result, err := tool.Execute(context.Background(), params)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	resultStr := result.(string)
	if contains(resultStr, "25 relevant") {
		t.Error("limit should be capped at 20, but all 25 records were returned")
	}
}

func TestMemorySearch_FormatResults(t *testing.T) {
	records := []memory.MemoryRecord{
		{
			ID:        "test-123",
			Type:      memory.MemoryTypeSession,
			Title:     "Test Memory",
			Content:   "This is test content",
			Tags:      []string{"tag1", "tag2"},
			Score:     0.92,
			CreatedAt: time.Now(),
		},
	}

	result := formatMemorySearchResults("test query", records)

	if !contains(result, "test query") {
		t.Error("formatted result should contain the query")
	}
	if !contains(result, "1 relevant memory record") {
		t.Error("formatted result should show correct count")
	}
	if !contains(result, "test-123") {
		t.Error("formatted result should contain record ID")
	}
	if !contains(result, "Session Memory") {
		t.Error("formatted result should contain type label")
	}
	if !contains(result, "Test Memory") {
		t.Error("formatted result should contain title")
	}
	if !contains(result, "tag1") || !contains(result, "tag2") {
		t.Error("formatted result should contain tags")
	}
	if !contains(result, "0.92") {
		t.Error("formatted result should contain score")
	}
	if !contains(result, "This is test content") {
		t.Error("formatted result should contain content")
	}
}

func TestMemoryTypeLabel(t *testing.T) {
	tests := []struct {
		input memory.MemoryType
		want  string
	}{
		{memory.MemoryTypeSession, "Session Memory"},
		{memory.MemoryTypeLongTerm, "Long-term Knowledge"},
		{memory.MemoryType(999), "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := memoryTypeLabel(tt.input)
			if got != tt.want {
				t.Errorf("memoryTypeLabel(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
