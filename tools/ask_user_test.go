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

// TestAskUser_OptionsDeclareStringItems 验证 options 是字符串数组：
// schema 必须带 items.type=string，否则模型会自由发挥成对象。
func TestAskUser_OptionsDeclareStringItems(t *testing.T) {
	info := NewAskUserTool().Info()
	for _, p := range info.Parameters {
		if p.Name != "options" {
			continue
		}
		if p.Type != "array" {
			t.Fatalf("options type = %q, want array", p.Type)
		}
		if p.Items == nil || p.Items.Type != "string" {
			t.Fatalf("options items = %#v, want string schema", p.Items)
		}
		if !strings.Contains(p.Description, "字符串") {
			t.Fatalf("options description should say 字符串: %q", p.Description)
		}
		return
	}
	t.Fatal("options parameter not found")
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

	// 应包含格式化后的提问消息
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
