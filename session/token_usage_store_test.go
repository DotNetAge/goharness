package session

import (
	"context"
	"testing"
	"time"
)

func TestTokenUsageStore_AppendAndQuery(t *testing.T) {
	store := NewInMemoryTokenUsageStore()
	ctx := context.Background()

	now := time.Now()
	records := []TokenUsageRecord{
		{
			ID:               NewRecordID(),
			SessionID:        "sess-1",
			ConversationID:   "conv-1",
			ModelName:        "gpt-4o",
			ProviderName:     "openai",
			AgentName:        "coder",
			PromptTokens:     1000,
			CompletionTokens: 500,
			CachedTokens:     200,
			TotalTokens:      1700,
			Timestamp:        now.Add(-2 * time.Minute),
		},
		{
			ID:               NewRecordID(),
			SessionID:        "sess-1",
			ConversationID:   "conv-2",
			ModelName:        "gpt-4o",
			ProviderName:     "openai",
			AgentName:        "coder",
			PromptTokens:     2000,
			CompletionTokens: 800,
			CachedTokens:     300,
			TotalTokens:      3100,
			Timestamp:        now.Add(-1 * time.Minute),
		},
		{
			ID:               NewRecordID(),
			SessionID:        "sess-2",
			ConversationID:   "conv-1",
			ModelName:        "claude-sonnet-4",
			ProviderName:     "anthropic",
			AgentName:        "architect",
			PromptTokens:     500,
			CompletionTokens: 200,
			TotalTokens:      700,
			Timestamp:        now,
		},
	}

	for _, r := range records {
		if err := store.Append(ctx, r); err != nil {
			t.Fatalf("Append failed: %v", err)
		}
	}

	// 查询 sess-1 的全部记录
	result, err := store.Query(ctx, TokenUsageFilter{SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 records for sess-1, got %d", len(result))
	}
	if result[0].PromptTokens != 1000 {
		t.Errorf("result[0].PromptTokens = %d, want 1000", result[0].PromptTokens)
	}
	if result[1].CompletionTokens != 800 {
		t.Errorf("result[1].CompletionTokens = %d, want 800", result[1].CompletionTokens)
	}

	// 按代理查询
	result, err = store.Query(ctx, TokenUsageFilter{AgentName: "architect"})
	if err != nil {
		t.Fatalf("Query by agent failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 record for architect, got %d", len(result))
	}

	// 按模型查询
	result, err = store.Query(ctx, TokenUsageFilter{ModelName: "gpt-4o"})
	if err != nil {
		t.Fatalf("Query by model failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 records for gpt-4o, got %d", len(result))
	}
}

func TestTokenUsageStore_EmptyQuery(t *testing.T) {
	store := NewInMemoryTokenUsageStore()
	ctx := context.Background()

	result, err := store.Query(ctx, TokenUsageFilter{SessionID: "nonexistent"})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d records", len(result))
	}
}

func TestTokenUsageStore_NoopStore(t *testing.T) {
	store := NewNoopTokenUsageStore()
	ctx := context.Background()

	if err := store.Append(ctx, TokenUsageRecord{}); err != nil {
		t.Errorf("Noop Append should not fail: %v", err)
	}
	result, err := store.Query(ctx, TokenUsageFilter{})
	if err != nil {
		t.Fatalf("Noop Query failed: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil result from Noop store, got %d records", len(result))
	}
}
