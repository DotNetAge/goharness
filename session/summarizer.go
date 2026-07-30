package session

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	gochat "github.com/DotNetAge/gochat"
	gochatcore "github.com/DotNetAge/gochat/core"

	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/memory"
)

// SummarizerOption 是 LLMSummarizer 的构造选项函数类型
type SummarizerOption func(*llmSummarizer)

// WithSystemPrompt 设置摘要器的自定义系统提示词。
// 如果不设置，将使用默认的摘要提示词。
func WithSystemPrompt(prompt string) SummarizerOption {
	return func(s *llmSummarizer) { s.systemPrompt = prompt }
}

// WithMaxTokens 设置摘要输出的最大 token 数。
// 默认值为 2048（多记忆片输出需要更多 token）。
//
// 注意：设置后 maxTokens 将固定为该值，不再随模型切换动态调整。
// 如需让 maxTokens 跟随当前模型的 MaxTokens 字段动态变化，
// 请勿调用此选项，改用 WithModelResolver 注入动态模型回调。
func WithMaxTokens(n int) SummarizerOption {
	return func(s *llmSummarizer) {
		s.maxTokens = n
		s.maxTokensOverride = true
	}
}

// WithModelResolver 注入一个动态获取当前模型的回调函数。
//
// 这是为了修复「Summarizer 不随模型切换更新」的设计缺陷：
// 旧实现将 model 配置在构造时固化为字段值，导致会话切换模型后
// 自动压缩仍使用旧模型生成摘要。
//
// 注入回调后，trySummarize 在每次摘要时都会调用 getModel()
// 读取最新的全局默认模型，保证摘要器与当前模型同步。
// 回调返回空 ModelConfig 时回退到构造时传入的固定 model。
func WithModelResolver(fn func() config.ModelConfig) SummarizerOption {
	return func(s *llmSummarizer) { s.getModel = fn }
}

// llmSummarizer 是基于 LLM 的摘要器实现，
// 将消息列表浓缩为多个结构化的 MemoryChunk。
//
// 模型获取策略（优先级从高到低）：
//  1. getModel 回调（动态，由 WithModelResolver 注入，跟随模型切换）
//  2. model 字段（固定，构造时传入的快照，用于无回调的旧场景/测试）
//
// maxTokens 计算策略：
//  1. 若通过 WithMaxTokens 显式覆盖（maxTokensOverride=true），使用固定值
//  2. 否则每次摘要时从当前模型的 MaxTokens 推导（>0 用模型值，否则 2048）
type llmSummarizer struct {
	model             config.ModelConfig
	getModel          func() config.ModelConfig
	systemPrompt      string
	maxTokens         int
	maxTokensOverride bool
}

// defaultSystemPrompt — 最简角色设定。格式指令放在最后一条 user message。
const defaultSystemPrompt = `你是记录员。将以下对话浓缩为要点式摘要 JSON 数组。不要推测缺失细节。`

// summarizeInstruction — 放在最后一条 user message，居于注意力峰值位置。
const summarizeInstruction = `将上面对话浓缩为 JSON 数组格式的要点式摘要。

输出格式：
[
  {
    "summary": "标题（≤20字，概括主题，用于检索召回）",
    "content": "要点式摘要",
    "tags": ["3-5个从内容提取的关键词，小写短横线分隔"],
    "timestamp": "最重要事件的 ISO 8601 时间（例如 2026-07-02T14:30:45Z）"
  }
]

content 要点式摘要（保留关键细节、路径、技术术语、重要资源）

示例：
[
  {
    "summary": "Redis 迁移",
    "content": "迁移到 Cluster 已确认",
    "tags": ["redis-migration"],
    "timestamp": "2026-07-02T14:30:45Z"
  },
  {
    "summary": "前端构建改造",
    "content": "构建工具切 Vite",
    "tags": ["vite"],
    "timestamp": "2026-07-03T09:15:00Z"
  }
]

规则：
- 只输出原始 JSON 数组，无其他文本
- 禁止对话体、问句、问候、情绪、表情符号
- 只记已确认事实，不含未决选项
- 跳过问候、确认词。矛盾只记最终方案
- 保留实体名（文件名、函数名、技术术语）
- tags 从 content 提取
- 无实质信息时返回 []
- 多主题拆成多条，单主题只输出一条`

// retryInstruction — 重试时使用的精简指令，同样放在最后一条 user message。
const retryInstruction = `将以上全部的对话输出为 JSON 数组。每条格式：{"summary":"标题","content":"要点","tags":["标签"],"timestamp":"ISO 8601"}。只输出 JSON 数组，无其他文本。无实质信息时返回 []。`

// NewLLMSummarizer 创建一个新的 LLM 摘要器实例。
//
// maxTokens 初值从 model.MaxTokens 推导（>0 用模型值，否则 2048），
// 可通过 WithMaxTokens 选项覆盖为固定值。
//
// 模型动态化：通过 WithModelResolver 注入回调后，每次摘要都会重新读取
// 当前模型配置（APIKey/BaseURL/Name/MaxTokens），保证切换模型后摘要器
// 立即生效。回调返回空 ModelConfig 时回退到本构造函数传入的固定 model。
func NewLLMSummarizer(model config.ModelConfig, opts ...SummarizerOption) Summarizer {
	// 优先使用模型配置的 max_tokens，确保摘要输出不会被截断
	maxTokens := 2048
	if model.MaxTokens > 0 {
		maxTokens = int(model.MaxTokens)
	}
	s := &llmSummarizer{
		model:        model,
		systemPrompt: defaultSystemPrompt,
		maxTokens:    maxTokens,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// resolveModel 返回当前摘要应使用的模型配置与 maxTokens。
//
// 模型来源优先级：getModel 回调 > 构造时传入的固定 model 字段。
// maxTokens 优先级：WithMaxTokens 显式覆盖 > 当前模型的 MaxTokens > 2048。
//
// 这是「Summarizer 随模型切换更新」的核心：每次摘要调用都重新解析，
// 不再使用构造时固化的快照。
func (s *llmSummarizer) resolveModel() (config.ModelConfig, int) {
	m := s.model
	if s.getModel != nil {
		if resolved := s.getModel(); resolved.Name != "" {
			m = resolved
		}
	}

	maxTokens := s.maxTokens
	if !s.maxTokensOverride {
		// 未显式覆盖：从当前模型重新推导
		if m.MaxTokens > 0 {
			maxTokens = int(m.MaxTokens)
		} else {
			maxTokens = 2048
		}
	}
	return m, maxTokens
}

// Summarize 调用 LLM 生成多个结构化的 MemoryChunk。
// 如果第一次调用返回非 JSON 输出，会自动重试一次，使用精简指令。
func (s *llmSummarizer) Summarize(ctx context.Context, messages []Message) ([]memory.MemoryChunk, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	// 第一次尝试
	chunks, err := s.trySummarize(ctx, messages, s.systemPrompt, summarizeInstruction)
	if err == nil {
		return chunks, nil
	}

	// 重试一次：使用更精简的指令
	chunks, retryErr := s.trySummarize(ctx, messages, s.systemPrompt, retryInstruction)
	if retryErr != nil {
		return nil, fmt.Errorf("summarizer: %w (retry also failed: %v)", err, retryErr)
	}
	return chunks, nil
}

// trySummarize 执行一次实际的 LLM 摘要调用。
// 消息结构：system(角色) → user(对话原文) → user(摘要指令)
// 摘要指令放在最后一条 user message 以获得最高注意力权重，类似 OpenCode 的做法。
//
// 模型动态化：每次调用都通过 resolveModel() 读取当前模型配置，
// 保证会话切换模型后摘要器立即使用新模型（APIKey/BaseURL/Name/MaxTokens 同步）。
func (s *llmSummarizer) trySummarize(ctx context.Context, messages []Message, systemPrompt, instruction string) ([]memory.MemoryChunk, error) {
	condensed := s.formatMessages(messages)

	msgs := []gochatcore.Message{
		gochatcore.NewSystemMessage(systemPrompt),
		gochatcore.NewUserMessage(condensed),
		gochatcore.NewUserMessage(instruction),
	}

	// 动态解析当前模型与 maxTokens，跟随模型切换
	model, maxTokens := s.resolveModel()

	resp, err := gochat.Client().
		Config(
			gochat.WithAPIKey(model.APIKey),
			gochat.WithBaseURL(model.BaseURL),
			gochat.WithTimeout(0), // 使用 context 控制超时
		).
		Messages(msgs...).
		Model(model.Name).
		MaxTokens(maxTokens).
		Temperature(0.3).
		WithContext(ctx).
		GetResponse()

	if err != nil {
		return nil, fmt.Errorf("summarizer: %w", err)
	}

	if resp == nil || resp.Content == "" {
		return nil, nil
	}

	return s.parseResponse(resp.Content)
}

// argKeyPriority 定义 summarization 时工具参数的关键字段。
// 匹配到这些字段时优先保留（其余字段仅在长度允许时附带），让 LLM 在生成摘要时
// 能拿到工具调用的"意图锚点"（操作哪个文件/执行什么命令/描述什么任务），而不被
// 长 JSON 字符串淹没。
var argKeyPriority = []string{
	"file_path", "path", "filepath",
	"command", "cmd",
	"subject", "description", "prompt", "query", "question",
	"pattern", "glob", "regex",
	"url", "uri",
	"old_string", "new_string",
}

// compressArguments 压缩工具调用参数：尝试解析为 JSON object，按优先级提取关键字段，
// 单个字段值超过 200 字符截断，整体超过 500 字符截断。
// 非 JSON 或解析失败时，直接按 500 字符截断。
func compressArguments(args string) string {
	if args == "" {
		return args
	}
	if len(args) <= 500 {
		return args
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(args), &obj); err != nil {
		return truncateStr(args, 500)
	}
	parts := make([]string, 0, len(obj))
	seen := make(map[string]bool, len(obj))
	for _, k := range argKeyPriority {
		v, ok := obj[k]
		if !ok {
			continue
		}
		s := fmt.Sprintf("%v", v)
		if len(s) > 200 {
			s = s[:200] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, s))
		seen[k] = true
	}
	for k, v := range obj {
		if seen[k] {
			continue
		}
		s := fmt.Sprintf("%v", v)
		if len(s) > 100 {
			s = s[:100] + "..."
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k, s))
	}
	result := strings.Join(parts, ", ")
	return truncateStr(result, 500)
}

// truncateStr 截断字符串，超过 maxLen 时在尾部追加 " ...(truncated)" 标记。
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + " ...(truncated)"
}

// rawChunk 是 parseResponse 解析 LLM 输出 JSON 时使用的中间结构体。
// Timestamp 是字符串（LLM 输出 ISO 8601），需在 parseResponse 中解析为 time.Time。
// 如果 LLM 不填或解析失败，MemoryChunk.Timestamp 保持零值，由调用方（session.generateSummary）fallback。
type rawChunk struct {
	Summary   string   `json:"summary"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	Timestamp string   `json:"timestamp,omitempty"`
}

// parseResponse 解析 LLM 返回的 JSON 数组响应为 MemoryChunk 列表。
func (s *llmSummarizer) parseResponse(response string) ([]memory.MemoryChunk, error) {
	text := s.preprocessJSON(response)
	if text == "" {
		return nil, nil
	}

	chunks, err := s.tryParseJSON(text)
	if err != nil {
		// 记录原始输出前 200 字符便于调试
		preview := text
		if len(preview) > 200 {
			preview = preview[:200]
		}
		return nil, fmt.Errorf("summarizer: LLM 输出不是有效的 JSON 数组 (preview: %q), discarded: %w", preview, err)
	}
	return chunks, nil
}

// preprocessJSON 清理和预处理 LLM 原始输出，提取 JSON 文本。
// 返回空字符串表示无实质信息。
func (s *llmSummarizer) preprocessJSON(response string) string {
	text := strings.TrimSpace(response)

	// 尝试提取 JSON（可能被 markdown 代码块包裹）
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = strings.TrimSpace(text[:idx])
		}
	}
	text = strings.TrimSpace(text)

	// 空响应、空对象、空数组 — LLM 判定无实质信息
	if text == "" || text == "{}" || text == "[]" {
		return ""
	}
	return text
}

// tryParseJSON 尝试将文本解析为 JSON 数组，失败时进行容错清洗后重试。
func (s *llmSummarizer) tryParseJSON(text string) ([]memory.MemoryChunk, error) {
	rawChunks, err := s.unmarshalJSON(text)
	if err != nil {
		return nil, err
	}
	return s.buildChunks(rawChunks), nil
}

// unmarshalJSON 尝试将文本解析为 rawChunk 数组。
// 首次失败时会对非法转义序列做清洗后重试。
func (s *llmSummarizer) unmarshalJSON(text string) ([]rawChunk, error) {
	var rawChunks []rawChunk
	if err := json.Unmarshal([]byte(text), &rawChunks); err != nil {
		// 清洗非法转义序列后重试（如 \   → 空格，\x → x）
		sanitized := sanitizeJSON(text)
		if sanitized != text {
			var retry []rawChunk
			if err2 := json.Unmarshal([]byte(sanitized), &retry); err2 == nil {
				return retry, nil
			}
		}
		return nil, err
	}
	return rawChunks, nil
}

// buildChunks 将 rawChunk 列表转为 MemoryChunk 列表，过滤空 content。
func (s *llmSummarizer) buildChunks(rawChunks []rawChunk) []memory.MemoryChunk {
	if len(rawChunks) == 0 {
		return nil
	}
	chunks := make([]memory.MemoryChunk, 0, len(rawChunks))
	for _, rc := range rawChunks {
		if rc.Content == "" {
			continue
		}
		chunks = append(chunks, buildChunkFromRaw(rc))
	}
	if len(chunks) == 0 {
		return nil
	}
	return chunks
}

// invalidJSONEscapeRe 匹配 JSON 字符串中反斜杠后跟非法转义字符的序列。
// 合法 JSON 转义：\" \\ \/ \b \f \n \r \t \u
var invalidJSONEscapeRe = regexp.MustCompile(`\\([^"\\/bfnrtu])`)

// sanitizeJSON 尝试修复 LLM 输出的常见 JSON 格式问题：
//   - 移除非法转义序列（如 \后跟空格 → 仅保留空格）
//
// 只对明显非法的序列做替换，不会改变合法 JSON。
func sanitizeJSON(text string) string {
	return invalidJSONEscapeRe.ReplaceAllString(text, "$1")
}

// buildChunkFromRaw 将 LLM 输出的 rawChunk 转为 MemoryChunk。
// Timestamp 解析 LLM 提供的 ISO 8601 字符串；解析失败保持零值，由上层 fallback。
func buildChunkFromRaw(rc rawChunk) memory.MemoryChunk {
	tags := rc.Tags
	if tags == nil {
		tags = []string{}
	}
	chunk := memory.MemoryChunk{
		Summary: rc.Summary,
		Content: rc.Content,
		Tags:    tags,
	}
	if rc.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, rc.Timestamp); err == nil {
			chunk.Timestamp = t
		}
	}
	return chunk
}

// formatMessages 将消息列表格式化为纯文本，供 LLM 摘要使用。
func (s *llmSummarizer) formatMessages(messages []Message) string {
	var buf strings.Builder
	for _, m := range messages {
		tsPrefix := ""
		if m.Timestamp > 0 {
			tsPrefix = "[" + time.Unix(m.Timestamp, 0).UTC().Format(time.RFC3339) + "] "
		}
		switch m.Role {
		case "user":
			buf.WriteString(tsPrefix + "[用户]\n")
			buf.WriteString(m.Content)
			buf.WriteString("\n\n")
		case "assistant":
			buf.WriteString(tsPrefix + "[助手]\n")
			buf.WriteString(m.Content)
			if len(m.ToolCalls) > 0 {
				buf.WriteString("\n[工具调用]")
				for _, tc := range m.ToolCalls {
					fmt.Fprintf(&buf, "\n- %s(%s)", tc.Name, tc.Arguments)
				}
			}
			buf.WriteString("\n\n")
		case "tool":
			buf.WriteString(fmt.Sprintf(tsPrefix+"[工具结果: %s]\n", m.ToolCallID))
			// 截断过长的工具结果
			if len(m.Content) > 2000 {
				buf.WriteString(m.Content[:2000])
				buf.WriteString("...(截断)")
			} else {
				buf.WriteString(m.Content)
			}
			buf.WriteString("\n\n")
		case "system":
			buf.WriteString(tsPrefix + "[系统]\n")
			buf.WriteString(m.Content)
			buf.WriteString("\n\n")
		default:
			buf.WriteString(fmt.Sprintf(tsPrefix+"[%s]\n", m.Role))
			buf.WriteString(m.Content)
			buf.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(buf.String())
}
