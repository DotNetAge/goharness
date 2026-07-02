package session

import (
	"context"
	"testing"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
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
	if summarizer.maxTokens != 2048 {
		t.Errorf("默认 maxTokens 应为 2048，得到 %d", summarizer.maxTokens)
	}
}

func TestLLMSummarizer_Summarize_EmptyMessages(t *testing.T) {
	model := config.ModelConfig{
		Name:    "test-model",
		APIKey:  "test-key",
		BaseURL: "https://test.example.com/v1",
	}

	s := NewLLMSummarizer(model)
	chunks, err := s.Summarize(context.Background(), nil)
	if err != nil {
		t.Errorf("空消息列表不应返回错误: %v", err)
	}
	if chunks != nil {
		t.Errorf("空消息列表应返回 nil，得到 %v", chunks)
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
	chunks, err := s.Summarize(context.Background(), nil)
	if err != nil {
		t.Errorf("nil 消息列表不应返回错误: %v", err)
	}
	if chunks != nil {
		t.Errorf("nil 消息列表应返回 nil，得到 %v", chunks)
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

// ─── compressArguments / truncateStr 单元测试 ─────────────────────────

func TestCompressArguments_Empty(t *testing.T) {
	if got := compressArguments(""); got != "" {
		t.Errorf("空字符串应原样返回，得到 %q", got)
	}
}

func TestCompressArguments_ShortUnchanged(t *testing.T) {
	args := `{"file_path":"src/foo.go","command":"ls"}`
	if got := compressArguments(args); got != args {
		// 短参数（< 500 字符）应原样返回,不进行解析
		t.Errorf("短参数应原样返回，得到 %q", got)
	}
}

func TestCompressArguments_KeyFieldsPriority(t *testing.T) {
	// 关键字段 file_path / command 应排在其他字段之前。
	// 用长 _meta 字段让总长 > 500 触发压缩逻辑。
	args := `{"_meta":"` + longString(500) + `","file_path":"src/foo.go","command":"ls"}`
	got := compressArguments(args)
	// 检查 file_path 出现在 _meta 之前
	idxFile := searchIndex(got, "file_path=")
	idxMeta := searchIndex(got, "_meta=")
	if idxFile < 0 {
		t.Fatalf("结果应包含 file_path 字段，得到 %q", got)
	}
	if idxMeta < 0 {
		t.Fatalf("结果应包含 _meta 字段，得到 %q", got)
	}
	if idxFile >= idxMeta {
		t.Errorf("file_path 应排在 _meta 之前，得到 %q", got)
	}
}

func TestCompressArguments_LongValueTruncated(t *testing.T) {
	// 单个字段值 > 200 字符应被截断
	longVal := make([]byte, 500)
	for i := range longVal {
		longVal[i] = 'A'
	}
	args := `{"file_path":"` + string(longVal) + `"}`
	got := compressArguments(args)
	if !contains(got, "...") {
		t.Errorf("超长字段值应被截断（带 ... 标记），得到 %q", got)
	}
}

func TestCompressArguments_OverallTruncated(t *testing.T) {
	// 多个关键字段组合后 > 500 字符应被整体截断。
	// 4 个 200 字符字段压缩后约 880 字符，远超 500。
	args := `{"file_path":"` + longString(200) +
		`","command":"` + longString(200) +
		`","description":"` + longString(200) +
		`","subject":"` + longString(200) + `"}`
	got := compressArguments(args)
	if len(got) > 600 { // 留 100 字符余量（marker 占用）
		t.Errorf("整体应被截断到 500 字符左右，得到长度 %d: %q", len(got), got)
	}
	if !contains(got, "truncated") {
		t.Errorf("超长结果应包含 truncated 标记，得到 %q", got)
	}
}

func TestCompressArguments_InvalidJSON(t *testing.T) {
	// 非 JSON 字符串（解析失败）走 500 字符截断
	args := "this is not json " + longString(600)
	got := compressArguments(args)
	if len(got) > 600 {
		t.Errorf("非 JSON 字符串应被截断，得到长度 %d", len(got))
	}
	if !contains(got, "truncated") {
		t.Errorf("非 JSON 字符串超长应包含 truncated 标记")
	}
}

func TestTruncateStr_NoTruncate(t *testing.T) {
	if got := truncateStr("hello", 100); got != "hello" {
		t.Errorf("短字符串应原样返回，得到 %q", got)
	}
}

func TestTruncateStr_Truncated(t *testing.T) {
	s := longString(500)
	got := truncateStr(s, 100)
	if len(got) > 120 {
		t.Errorf("截断后长度应接近 maxLen，得到 %d", len(got))
	}
	if !contains(got, "truncated") {
		t.Error("截断结果应包含 truncated 标记")
	}
}

// longString 生成 n 字符的 'A' 字符串
func longString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = 'A'
	}
	return string(b)
}

// searchIndex 返回 substr 在 s 中首次出现的位置，未找到返回 -1
func searchIndex(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// ─── toLLMMessages 时间戳前缀测试 ─────────────────────────

func TestToLLMMessages_TimestampPrefix(t *testing.T) {
	model := config.ModelConfig{Name: "test", APIKey: "key"}
	s := NewLLMSummarizer(model).(*llmSummarizer)

	ts := time.Date(2026, 7, 2, 14, 35, 0, 0, time.UTC).Unix()
	messages := []Message{
		{Role: "user", Content: "你好", Timestamp: ts},
		{Role: "assistant", Content: "有什么可以帮你的？", Timestamp: ts + 5},
	}

	result := s.toLLMMessages(messages)
	if len(result) != 2 {
		t.Fatalf("应返回 2 条消息，得到 %d", len(result))
	}

	// 第一条 user 消息的 Content 应包含 ISO 8601 时间戳前缀
	if result[0].Role != gochatcore.RoleUser {
		t.Errorf("第一条应为 user 角色，得到 %v", result[0].Role)
	}
	if !contains(result[0].Content[0].Text, "2026-07-02T14:35:00Z") {
		t.Errorf("user 消息应包含时间戳前缀，得到 %q", result[0].Content[0].Text)
	}
	if !contains(result[0].Content[0].Text, "你好") {
		t.Errorf("user 消息应保留原始内容")
	}

	// 第二条 assistant 消息同样应带时间戳
	if result[1].Role != gochatcore.RoleAssistant {
		t.Errorf("第二条应为 assistant 角色，得到 %v", result[1].Role)
	}
}

func TestToLLMMessages_NoTimestampSkipsPrefix(t *testing.T) {
	model := config.ModelConfig{Name: "test", APIKey: "key"}
	s := NewLLMSummarizer(model).(*llmSummarizer)

	messages := []Message{
		{Role: "user", Content: "你好"}, // Timestamp 默认为 0
	}

	result := s.toLLMMessages(messages)
	text := result[0].Content[0].Text
	// 不应有 "[" 开头的 ISO 时间戳前缀（如果内容里碰巧有 "[" 不在此断言范围内）
	if contains(text, "T00:00:00Z") || contains(text, "[1970-") {
		t.Errorf("Timestamp=0 时不应生成前缀，得到 %q", text)
	}
	if text != "你好" {
		t.Errorf("应原样保留内容，得到 %q", text)
	}
}

// ─── parseResponse 时间戳解析测试 ─────────────────────────

func TestParseResponse_TimestampParsed(t *testing.T) {
	model := config.ModelConfig{Name: "test", APIKey: "key"}
	s := NewLLMSummarizer(model).(*llmSummarizer)

	response := `[{"summary":"测试摘要","content":"测试内容","tags":["test"],"timestamp":"2026-07-02T14:30:45Z"}]`
	chunks, err := s.parseResponse(response)
	if err != nil {
		t.Fatalf("parseResponse 失败: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("应返回 1 个 chunk，得到 %d", len(chunks))
	}
	want, _ := time.Parse(time.RFC3339, "2026-07-02T14:30:45Z")
	if !chunks[0].Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", chunks[0].Timestamp, want)
	}
}

func TestParseResponse_TimestampMissing(t *testing.T) {
	model := config.ModelConfig{Name: "test", APIKey: "key"}
	s := NewLLMSummarizer(model).(*llmSummarizer)

	// LLM 没填 timestamp 字段
	response := `[{"summary":"测试摘要","content":"测试内容","tags":["test"]}]`
	chunks, err := s.parseResponse(response)
	if err != nil {
		t.Fatalf("parseResponse 失败: %v", err)
	}
	if !chunks[0].Timestamp.IsZero() {
		t.Errorf("LLM 未填 timestamp 时应保持零值，得到 %v", chunks[0].Timestamp)
	}
}

func TestParseResponse_TimestampInvalid(t *testing.T) {
	model := config.ModelConfig{Name: "test", APIKey: "key"}
	s := NewLLMSummarizer(model).(*llmSummarizer)

	// LLM 填了非法格式的时间戳
	response := `[{"summary":"测试摘要","content":"测试内容","tags":["test"],"timestamp":"not-a-date"}]`
	chunks, err := s.parseResponse(response)
	if err != nil {
		t.Fatalf("parseResponse 失败: %v", err)
	}
	if !chunks[0].Timestamp.IsZero() {
		t.Errorf("timestamp 解析失败时应保持零值，得到 %v", chunks[0].Timestamp)
	}
}
