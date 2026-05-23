package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBudgetCompact_NilEstimateFn(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "short"},
		{Role: "assistant", Content: "also short"},
	}
	result := BudgetCompact(messages, nil, 10)
	if len(result) != 2 {
		t.Errorf("expected 2 messages, got %d", len(result))
	}
}

func TestBudgetCompact_WithinBudget(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "user", Content: "how are you"},
	}
	result := BudgetCompact(messages, func(s string) int { return len(s) }, 1000)
	if len(result) != 3 {
		t.Errorf("expected all 3 messages preserved when within budget")
	}
}

func TestBudgetCompact_TruncatesLargeMessages(t *testing.T) {
	largeContent := make([]byte, 9000)
	for i := range largeContent {
		largeContent[i] = 'A'
	}

	messages := []Message{
		{Role: "user", Content: "small msg 1"},
		{Role: "assistant", Content: string(largeContent)},
		{Role: "user", Content: "small msg 2"},
	}
	result := BudgetCompact(messages, func(s string) int { return len(s) / 3 }, 500)

	found := false
	for _, m := range result {
		if len(m.Content) < len(largeContent) && len(m.Content) > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected large message to be truncated or skipped")
	}
	totalTokens := int64(0)
	for _, m := range result {
		totalTokens += int64(len(m.Content) / 3)
	}
	if totalTokens > 500*2 {
		t.Errorf("total tokens after compact should be near budget, got %d", totalTokens)
	}
}

func TestBudgetCompact_PreservesOrder(t *testing.T) {
	messages := []Message{
		{Role: "user", Content: "msg-001-first"},
		{Role: "assistant", Content: "msg-002-second"},
		{Role: "user", Content: "msg-003-third"},
		{Role: "assistant", Content: "msg-004-fourth"},
	}
	result := BudgetCompact(messages, func(s string) int { return len(s) }, 20)
	if len(result) == 0 {
		t.Fatal("expected at least some messages")
	}
	for i := 1; i < len(result); i++ {
		if result[i].Content <= result[i-1].Content {
			t.Errorf("messages not in chronological order at index %d: %q before %q", i, result[i-1].Content, result[i].Content)
		}
	}
}

func TestBudgetCompact_ShortInput(t *testing.T) {
	messages := []Message{{Role: "user", Content: "only one"}}
	result := BudgetCompact(messages, nil, 1)
	if len(result) != 1 {
		t.Errorf("single message should pass through")
	}
	messages2 := []Message{}
	result2 := BudgetCompact(messages2, nil, 1)
	if len(result2) != 0 {
		t.Errorf("empty input should return empty")
	}
}

func TestDefaultTokenEstimator(t *testing.T) {
	// NewDefaultTokenEstimator now delegates to EstimateTokens (tiktoken-backed)
	e := NewDefaultTokenEstimator(4.0)
	count := e.Estimate("hello world")
	// "hello world" = 2 tokens in BPE (or ~3 with heuristic fallback)
	if count < 1 || count > 5 {
		t.Errorf("expected reasonable token count for 'hello world', got %d", count)
	}
	// Verify backward-compatible constructor works
	e2 := NewDefaultTokenEstimator(-1)
	if e2.Estimate("test") < 1 {
		t.Errorf("negative charsPerToken should still produce valid estimate, got %d", e2.Estimate("test"))
	}
	e3 := NewDefaultTokenEstimator(0)
	if e3.Estimate("test") < 1 {
		t.Errorf("zero charsPerToken should still produce valid estimate, got %d", e3.Estimate("test"))
	}
}

func TestTrimJSONResult_ShortString(t *testing.T) {
	input := `{"key": "value"}`
	result := TrimJSONResult(input, 100)
	if result != input {
		t.Errorf("short JSON should pass through unchanged")
	}
}

func TestTrimJSONResult_TrimObject(t *testing.T) {
	largeObj := make(map[string]string)
	for i := 0; i < 50; i++ {
		largeObj[fmt.Sprintf("field_%d", i)] = strings.Repeat("x", 200)
	}
	data, _ := json.Marshal(largeObj)
	input := string(data)

	result := TrimJSONResult(input, 500)
	if len(result) > 550 {
		t.Errorf("trimmed result should be ~500 chars, got %d", len(result))
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		t.Errorf("result should still be valid JSON: %v", err)
	}
}

func TestTrimJSONResult_TrimArray(t *testing.T) {
	arr := make([]string, 30)
	for i := range arr {
		arr[i] = string(make([]byte, 200))
	}
	data, _ := json.Marshal(arr)
	input := string(data)

	result := TrimJSONResult(input, 1000)
	if len(result) > 1100 {
		t.Errorf("trimmed array result too long: %d", len(result))
	}
}

func TestTrimJSONResult_NonJSON(t *testing.T) {
	longStr := strings.Repeat("a", 1000)
	result := TrimJSONResult(longStr, 500)
	if len(result) > 520 {
		t.Errorf("non-JSON should be truncated simply, got %d", len(result))
	}
}

func TestTrimJSONResult_TrimLongStrings(t *testing.T) {
	obj := map[string]any{"data": strings.Repeat("y", 1000)}
	data, _ := json.Marshal(obj)
	result := TrimJSONResult(string(data), 200)
	if len(result) > 220 {
		t.Errorf("long string values should be trimmed, got %d", len(result))
	}
}
