package tools

import (
	"context"
	"strings"
	"testing"
)

func TestAskUser_Info(t *testing.T) {
	tool := NewAskUserTool()
	info := tool.Info()

	if info.Name != "AskUser" {
		t.Errorf("expected tool name 'AskUser', got %q", info.Name)
	}
	if info.IsReadOnly {
		t.Error("expected IsReadOnly to be false (uses permission system)")
	}
	if len(info.Parameters) == 0 {
		t.Error("expected parameters to be defined")
	}

	var hasQuestion bool
	for _, p := range info.Parameters {
		if p.Name == "question" && p.Required {
			hasQuestion = true
		}
	}
	if !hasQuestion {
		t.Error("expected 'question' parameter to be required")
	}

	if len(info.Tags) == 0 {
		t.Error("expected Tags to be defined")
	}
}

func TestAskUser_ExecuteWithoutAnswers(t *testing.T) {
	tool := NewAskUserTool()

	result, err := tool.Execute(ctxWithLogger(), map[string]any{
		"question": "What is your name?",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	str, ok := result.(string)
	if !ok {
		t.Fatalf("expected string result, got %T", result)
	}
	if !strings.Contains(str, "What is your name?") {
		t.Errorf("result should contain the question, got: %s", str)
	}
}

func TestAskUser_ExecuteWithAnswers(t *testing.T) {
	tool := NewAskUserTool()

	result, err := tool.Execute(ctxWithLogger(), map[string]any{
		"question": "What is your name?",
		"answers": map[string]any{
			"What is your name?": "Alice",
		},
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	str, ok := result.(string)
	if !ok {
		t.Fatalf("expected string result, got %T", result)
	}

	// Should contain the formatted question message
	if !strings.Contains(str, "已向用户提问") {
		t.Errorf("expected question summary, got: %s", str)
	}
	if !strings.Contains(str, "What is your name") {
		t.Errorf("expected question value in result, got: %s", str)
	}
	if !strings.Contains(str, "等待他们的回复") {
		t.Errorf("expected guidance for LLM, got: %s", str)
	}
}

func TestAskUser_MissingParam(t *testing.T) {
	tool := NewAskUserTool()

	_, err := tool.Execute(ctxWithLogger(), map[string]any{})
	if err == nil {
		t.Error("expected error for missing question parameter")
	}
}

func TestAskUser_EmptyQuestion(t *testing.T) {
	tool := NewAskUserTool()

	_, err := tool.Execute(ctxWithLogger(), map[string]any{
		"question": "",
	})
	if err == nil {
		t.Error("expected error for empty question parameter")
	}
}

func TestAskUser_ExecuteIsNonBlocking(t *testing.T) {
	tool := NewAskUserTool()

	done := make(chan struct{})
	go func() {
		defer close(done)
		tool.Execute(ctxWithLogger(), map[string]any{"question": "test"})
	}()

	select {
	case <-done:
	case <-context.Background().Done():
		t.Fatal("Execute blocked - expected non-blocking return")
	}
}

func TestAskUser_NoInteractionRequest(t *testing.T) {
	tool := NewAskUserTool()

	result, err := tool.Execute(ctxWithLogger(), map[string]any{
		"question": "test?",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	// Must NOT contain _interaction key (side channel is removed)
	if _, ok := result.(map[string]any); ok {
		t.Fatal("result should not be a map — _interaction side channel has been removed")
	}
}
