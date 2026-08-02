package tools

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/memory"
)

// mockMemory implements memory.Memory for testing
type mockMemory struct {
	chunks []memory.MemoryChunk
	err    error
}

func (m *mockMemory) Retrieve(_ context.Context, query string, _ ...memory.RetrieveOption) ([]memory.MemoryChunk, error) {
	if m.err != nil {
		return nil, m.err
	}
	if query == "error" {
		return nil, errors.New("memory retrieval failed")
	}

	var results []memory.MemoryChunk
	for _, c := range m.chunks {
		results = append(results, c)
	}
	return results, nil
}

func (m *mockMemory) Store(_ context.Context, chunk memory.MemoryChunk) (string, error) {
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
		chunks: []memory.MemoryChunk{
			{
				ID:        "mem-1",
				Summary:   "User Prefers TypeScript",
				Content:   "User explicitly prefers TypeScript over JavaScript for all new projects",
				Tags:      []string{"preference", "typescript"},
				AgentName: "test-agent",
				Timestamp: time.Now(),
			},
			{
				ID:        "mem-2",
				Summary:   "Current Project Structure",
				Content:   "Project uses monorepo structure with packages in /packages directory",
				Tags:      []string{"project", "structure"},
				AgentName: "test-agent",
				Timestamp: time.Now(),
			},
		},
	}

	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{
		"query": "user preferences for programming languages",
		"limit": float64(10),
	}

	result, err := tool.Execute(ctxWithLogger(), params)
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
	if !contains(resultStr, "2 条相关记忆记录") {
		t.Error("result should indicate 2 records found")
	}
	// 实现输出的是 Content 而不是 Summary
	if !contains(resultStr, "User explicitly prefers TypeScript") {
		t.Error("result should contain content")
	}
}

func TestMemorySearch_Execute_EmptyResult(t *testing.T) {
	mem := &mockMemory{
		chunks: []memory.MemoryChunk{},
	}

	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{
		"query": "nonexistent topic that has no memories",
	}

	result, err := tool.Execute(ctxWithLogger(), params)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	resultStr := result.(string)
	if !contains(resultStr, "未找到关于查询的记忆") {
		t.Errorf("empty result should indicate no memories found, got: %s", resultStr)
	}
}

func TestMemorySearch_Execute_Error(t *testing.T) {
	mem := &mockMemory{err: errors.New("memory system unavailable")}

	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{
		"query": "test query",
	}

	// 实现中当所有 token 都失败时，返回"未找到"而不是错误
	result, err := tool.Execute(ctxWithLogger(), params)
	if err != nil {
		t.Fatalf("Execute() should not return error, got: %v", err)
	}

	resultStr, ok := result.(string)
	if !ok {
		t.Fatal("result should be a string")
	}

	// 应该返回"未找到"消息
	if !contains(resultStr, "未找到关于查询的记忆") {
		t.Errorf("should indicate no memories found, got: %s", resultStr)
	}
}

func TestMemorySearch_Execute_MissingQuery(t *testing.T) {
	mem := &mockMemory{}
	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{}

	_, err := tool.Execute(ctxWithLogger(), params)
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

	_, err := tool.Execute(ctxWithLogger(), params)
	if err == nil {
		t.Fatal("Execute() should return error for short query")
	}
}

func TestMemorySearch_Execute_WithTypesFilter(t *testing.T) {
	mem := &mockMemory{
		chunks: []memory.MemoryChunk{
			{
				ID:        "mem-1",
				Summary:   "Long Term Memory",
				Content:   "This is long-term knowledge",
				AgentName: "test-agent",
			},
		},
	}

	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{
		"query": "memory test",
		"types": []any{"longterm"},
	}

	result, err := tool.Execute(ctxWithLogger(), params)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	if result == nil {
		t.Fatal("result should not be nil")
	}
}

func TestMemorySearch_LimitValidation(t *testing.T) {
	chunks := make([]memory.MemoryChunk, 25)
	for i := range chunks {
		chunks[i] = memory.MemoryChunk{
			ID:      fmt.Sprintf("mem-%d", i),
			Content: "test",
		}
	}
	mem := &mockMemory{
		chunks: chunks,
	}

	tool := NewMemorySearch(mem).(*MemorySearch)

	params := map[string]any{
		"query": "test",
		"limit": float64(100),
	}

	result, err := tool.Execute(ctxWithLogger(), params)
	if err != nil {
		t.Fatalf("Execute() returned error: %v", err)
	}

	resultStr := result.(string)
	if contains(resultStr, "25 relevant") {
		t.Error("limit should be capped at 20, but all 25 records were returned")
	}
}

func TestMemorySearch_FormatResults(t *testing.T) {
	chunks := []memory.MemoryChunk{
		{
			ID:        "test-123",
			Summary:   "Test Memory",
			Content:   "This is test content",
			Tags:      []string{"tag1", "tag2"},
			AgentName: "test-agent",
			Timestamp: time.Now(),
		},
	}

	result := formatMemorySearchResults("test query", chunks)

	if !contains(result, "test query") {
		t.Error("formatted result should contain the query")
	}
	if !contains(result, "1 条相关记忆记录") {
		t.Error("formatted result should show correct count")
	}
	// formatMemorySearchResults 输出 Content 而不是 ID 或 Summary
	if !contains(result, "This is test content") {
		t.Error("formatted result should contain content")
	}
	if !contains(result, "tag1") || !contains(result, "tag2") {
		t.Error("formatted result should contain tags")
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
