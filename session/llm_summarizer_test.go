package session

import (
	"context"
	"testing"

	"github.com/DotNetAge/goharness/config"
)

func TestNewLLMSummarizer_RequiredModelConfig(t *testing.T) {
	model := config.ModelConfig{
		Name:    "test-model",
		APIKey:  "test-key",
		BaseURL: "https://test.example.com/v1",
	}

	s := NewLLMSummarizer(model)
	if s == nil {
		t.Fatal("NewLLMSummarizer() returned nil")
	}

	// 验证实现了 Summarizer 接口
	var _ Summarizer = s
}

func TestNewLLMSummarizer_WithSystemPrompt(t *testing.T) {
	model := config.ModelConfig{
		Name:   "test-model",
		APIKey: "test-key",
	}
	customPrompt := "请用英文摘要以下内容"

	s := NewLLMSummarizer(model, WithSystemPrompt(customPrompt))
	summarizer, ok := s.(*llmSummarizer)
	if !ok {
		t.Fatal("类型断言失败")
	}
	if summarizer.systemPrompt != customPrompt {
		t.Errorf("systemPrompt = %q, want %q", summarizer.systemPrompt, customPrompt)
	}
}

func TestNewLLMSummarizer_WithMaxTokens(t *testing.T) {
	model := config.ModelConfig{
		Name:   "test-model",
		APIKey: "test-key",
	}

	s := NewLLMSummarizer(model, WithMaxTokens(2048))
	summarizer, ok := s.(*llmSummarizer)
	if !ok {
		t.Fatal("类型断言失败")
	}
	if summarizer.maxTokens != 2048 {
		t.Errorf("maxTokens = %d, want %d", summarizer.maxTokens, 2048)
	}
}

func TestNewLLMSummarizer_DefaultValues(t *testing.T) {
	model := config.ModelConfig{
		Name:   "test-model",
		APIKey: "test-key",
	}

	s := NewLLMSummarizer(model)
	summarizer, ok := s.(*llmSummarizer)
	if !ok {
		t.Fatal("类型断言失败")
	}
	if summarizer.systemPrompt != defaultSystemPrompt {
		t.Errorf("默认 systemPrompt 应为 defaultSystemPrompt，得到 %q", summarizer.systemPrompt)
	}
	if summarizer.maxTokens != 1024 {
		t.Errorf("默认 maxTokens 应为 1024，得到 %d", summarizer.maxTokens)
	}
}

func TestLLMSummarizer_Summarize_EmptyMessages(t *testing.T) {
	model := config.ModelConfig{
		Name:    "test-model",
		APIKey:  "test-key",
		BaseURL: "https://test.example.com/v1",
	}

	s := NewLLMSummarizer(model)
	summary, err := s.Summarize(context.Background(), nil)
	if err != nil {
		t.Errorf("空消息列表不应返回错误: %v", err)
	}
	if summary != "" {
		t.Errorf("空消息列表应返回空字符串，得到 %q", summary)
	}
}

func TestLLMSummarizer_formatMessages_UserAndAssistant(t *testing.T) {
	model := config.ModelConfig{Name: "test", APIKey: "key"}
	s := NewLLMSummarizer(model).(*llmSummarizer)

	messages := []Message{
		{Role: "user", Content: "你好"},
		{Role: "assistant", Content: "你好！有什么可以帮你的？"},
	}

	result := s.formatMessages(messages)
	if result == "" {
		t.Fatal("formatMessages() 返回空字符串")
	}
	// 验证包含角色标记
	if !contains(result, "[用户]") {
		t.Error("结果应包含 [用户] 标记")
	}
	if !contains(result, "[助手]") {
		t.Error("结果应包含 [助手] 标记")
	}
	if !contains(result, "你好") {
		t.Error("结果应包含用户消息内容")
	}
}

func TestLLMSummarizer_formatMessages_WithToolCalls(t *testing.T) {
	model := config.ModelConfig{Name: "test", APIKey: "key"}
	s := NewLLMSummarizer(model).(*llmSummarizer)

	messages := []Message{
		{
			Role:    "assistant",
			Content: "我来帮你搜索一下",
			ToolCalls: []ToolCall{
				{ID: "call_1", Name: "WebSearch", Arguments: `{"query":"golang"}`},
			},
		},
		{Role: "tool", Content: `[{"name":"WebSearch","result":"Go is a programming language"}]`, ToolCallID: "call_1"},
	}

	result := s.formatMessages(messages)
	if !contains(result, "[工具调用]") {
		t.Error("应包含工具调用标记")
	}
	if !contains(result, "WebSearch") {
		t.Error("应包含工具名称")
	}
	if !contains(result, "[工具结果:") {
		t.Error("应包含工具结果标记")
	}
}

func TestLLMSummarizer_formatMessages_LongToolResultTruncated(t *testing.T) {
	model := config.ModelConfig{Name: "test", APIKey: "key"}
	s := NewLLMSummarizer(model).(*llmSummarizer)

	longContent := make([]byte, 3000)
	for i := range longContent {
		longContent[i] = 'A'
	}

	messages := []Message{
		{Role: "tool", Content: string(longContent), ToolCallID: "call_1"},
	}

	result := s.formatMessages(messages)
	if !contains(result, "(截断)") {
		t.Error("超长工具结果应被截断并标记")
	}
}

func TestLLMSummarizer_formatMessages_SystemMessage(t *testing.T) {
	model := config.ModelConfig{Name: "test", APIKey: "key"}
	s := NewLLMSummarizer(model).(*llmSummarizer)

	messages := []Message{
		{Role: "system", Content: "你是一个有帮助的助手"},
	}

	result := s.formatMessages(messages)
	if !contains(result, "[系统]") {
		t.Error("应包含系统消息标记")
	}
}

func TestLLMSummarizer_Summarize_InterfaceCompliance(t *testing.T) {
	// 验证 LLMSummarizer 完全满足 Summarizer 接口
	model := config.ModelConfig{
		Name:    "test-model",
		APIKey:  "test-key",
		BaseURL: "https://test.example.com/v1",
	}

	s := NewLLMSummarizer(model)
	// 编译期接口检查
	var _ Summarizer = s

	// 空消息列表应安全返回
	summary, err := s.Summarize(context.Background(), nil)
	if err != nil {
		t.Errorf("nil 消息列表不应返回错误: %v", err)
	}
	if summary != "" {
		t.Errorf("nil 消息列表应返回空字符串，得到 %q", summary)
	}
}

// contains 检查字符串是否包含子串
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
