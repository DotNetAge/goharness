package session

import (
	"context"
	"encoding/json"
	"fmt"
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
func WithMaxTokens(n int) SummarizerOption {
	return func(s *llmSummarizer) { s.maxTokens = n }
}

// llmSummarizer 是基于 LLM 的摘要器实现，
// 将消息列表浓缩为多个结构化的 MemoryChunk。
type llmSummarizer struct {
	model        config.ModelConfig
	systemPrompt string
	maxTokens    int
}

// defaultSystemPrompt is the default summarizer system prompt.
// Using English reduces token usage compared to Chinese.
const defaultSystemPrompt = `You are a conversation summarizer. Condense the following preprocessed conversation into independent memory chunks.

NOTE: The input has been preprocessed — tool arguments are reduced to key fields (file_path, command, subject, etc.), tool results are truncated to 2000 characters. Do NOT attempt to restore, guess, or speculate about missing details.

Each chunk is a self-contained knowledge unit: it must remain understandable and usable without the original conversation context. The system automatically attaches session_id and agent_name to each chunk — do NOT include these. Each input message has an ISO 8601 timestamp prefix (e.g., "[2026-07-02T14:30:45Z]") which you can reference to determine when events occurred.

Each chunk has four fields:
1. summary: 8–15 words capturing the gist (used only for semantic retrieval; concise for embedding precision)
2. content: Complete memory content — all key information of this unit, independently understandable. Do NOT repeat facts already stated in summary.
3. tags: 3–5 keywords, lowercase, kebab-case (e.g., "auth-flow", "goharness"), distinctive for topic-based filtering
4. timestamp: ISO 8601 datetime of when the described event occurred (e.g., "2026-07-02T14:30:45Z"). Reference the input message prefixes; if the chunk spans multiple times, use the most significant event's time.

Splitting rules:
- Split into separate chunks when: independent topics, independent decisions, or independent knowledge units
- Merge into one chunk when: a decision with its rationale, or a problem with its resolution
- Target 3–8 chunks per call; never exceed 12

Content priority (include in this order; drop lower-priority items if length-constrained):
1. Decisions and their conclusions
2. Explicit user preferences and constraints
3. Technical facts: file paths, function names, key code snippets
4. Procedural chitchat (drop unless central to the topic)

Quality rules:
- Skip trivial exchanges (greetings, single-word acknowledgments like "ok" / "好的", pure test inputs) — return fewer chunks rather than padding with low-value content
- When a later message contradicts an earlier one (e.g., "no, that's wrong", "不对", "重做", "再试一次"), summarize the FINAL resolution only, NOT the rejected attempts
- If the user expresses dissatisfaction with a result, do NOT memorize the rejected version as a fact
- Prefer one high-quality chunk over several low-quality ones
- If after filtering trivial content the conversation has no substantive information, return an empty array

Strict output:
- Output a raw JSON array only — no markdown code blocks, no extra text
- Each chunk must have non-empty summary AND non-empty content
- Return an empty array if the conversation is trivial (e.g., casual greetings)
- Summary language must match the language used in the conversation
- Do not fabricate; do not omit key decisions or conclusions
- Do not duplicate facts between summary and content

Output format (strict JSON array):
[
  {
    "summary": "8–15 word gist",
    "content": "Complete self-contained memory content",
    "tags": ["distinctive-tag-1", "distinctive-tag-2", "distinctive-tag-3"],
    "timestamp": "2026-07-02T14:30:45Z"
  }
]`

// NewLLMSummarizer 创建一个新的 LLM 摘要器实例。
func NewLLMSummarizer(model config.ModelConfig, opts ...SummarizerOption) Summarizer {
	s := &llmSummarizer{
		model:        model,
		systemPrompt: defaultSystemPrompt,
		maxTokens:    2048,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Summarize 调用 LLM 生成多个结构化的 MemoryChunk。
// 直接将原始消息序列（保留原始 role/content）传递给 LLM，避免预格式化带来的 token 膨胀和超限风险。
func (s *llmSummarizer) Summarize(ctx context.Context, messages []Message) ([]memory.MemoryChunk, error) {
	if len(messages) == 0 {
		return nil, nil
	}

	// 拼接消息列表：系统提示词 + 原始对话消息
	msgs := make([]gochatcore.Message, 0, len(messages)+1)
	msgs = append(msgs, gochatcore.NewSystemMessage(s.systemPrompt))
	msgs = append(msgs, s.toLLMMessages(messages)...)

	resp, err := gochat.Client().
		Config(
			gochat.WithAPIKey(s.model.APIKey),
			gochat.WithBaseURL(s.model.BaseURL),
			gochat.WithTimeout(0), // 使用 context 控制超时
		).
		Messages(msgs...).
		Model(s.model.Name).
		MaxTokens(s.maxTokens).
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

// toLLMMessages 将 session.Message 列表转换为 gochatcore.Message 列表，
// 保留原始角色与内容，但压缩工具信息以控制 summarization 的 token 开销：
//   - ToolCalls.Arguments: 提取关键字段（file_path/command/subject/...）后截断到 500 字符
//   - tool Content: 截断到 2000 字符
//
// 工具名与工具结果的事实信息保留，因为它们往往是摘要需要回顾的核心上下文。
func (s *llmSummarizer) toLLMMessages(messages []Message) []gochatcore.Message {
	out := make([]gochatcore.Message, 0, len(messages))
	for _, m := range messages {
		tsPrefix := ""
		if m.Timestamp > 0 {
			tsPrefix = "[" + time.Unix(m.Timestamp, 0).UTC().Format(time.RFC3339) + "] "
		}
		switch m.Role {
		case "user":
			out = append(out, gochatcore.NewUserMessage(tsPrefix+m.Content))
		case "assistant":
			msg := gochatcore.Message{
				Role: gochatcore.RoleAssistant,
				Content: []gochatcore.ContentBlock{
					{Type: gochatcore.ContentTypeText, Text: tsPrefix + m.Content},
				},
			}
			if len(m.ToolCalls) > 0 {
				tcs := make([]gochatcore.ToolCall, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					tcs[i] = gochatcore.ToolCall{
						ID:        tc.ID,
						Name:      tc.Name,
						Arguments: compressArguments(tc.Arguments),
					}
				}
				msg.ToolCalls = tcs
			}
			out = append(out, msg)
		case "tool":
			msg := gochatcore.Message{
				Role: gochatcore.RoleTool,
				Content: []gochatcore.ContentBlock{
					{Type: gochatcore.ContentTypeText, Text: tsPrefix + truncateStr(m.Content, 2000)},
				},
				ToolCallID: m.ToolCallID,
			}
			out = append(out, msg)
		case "system":
			out = append(out, gochatcore.NewSystemMessage(tsPrefix+m.Content))
		}
	}
	return out
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

// parseResponse 解析 LLM 返回的 JSON 响应为 MemoryChunk 列表。
func (s *llmSummarizer) parseResponse(response string) ([]memory.MemoryChunk, error) {
	text := strings.TrimSpace(response)

	// 尝试提取 JSON 数组（可能被 markdown 代码块包裹）
	if strings.HasPrefix(text, "```") {
		// 移除 ```json 或 ``` 包裹
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = strings.TrimSpace(text[:idx])
		}
	}
	text = strings.TrimSpace(text)

	// 解析 JSON 数组
	var rawChunks []rawChunk
	if err := json.Unmarshal([]byte(text), &rawChunks); err != nil {
		// 如果解析失败，将整个响应作为单个记忆片的内容
		return []memory.MemoryChunk{
			{
				Summary: "对话摘要",
				Content: text,
				Tags:    []string{},
			},
		}, nil
	}

	if len(rawChunks) == 0 {
		return nil, nil
	}

	chunks := make([]memory.MemoryChunk, 0, len(rawChunks))
	for _, rc := range rawChunks {
		if rc.Content == "" {
			continue
		}
		tags := rc.Tags
		if tags == nil {
			tags = []string{}
		}
		chunk := memory.MemoryChunk{
			Summary: rc.Summary,
			Content: rc.Content,
			Tags:    tags,
		}
		// 解析 LLM 提供的 ISO 8601 时间戳；解析失败保持零值，由上层 fallback
		if rc.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339, rc.Timestamp); err == nil {
				chunk.Timestamp = t
			}
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}

// formatMessages 将消息列表格式化为纯文本，供 LLM 摘要使用。
func (s *llmSummarizer) formatMessages(messages []Message) string {
	var buf strings.Builder
	for _, m := range messages {
		switch m.Role {
		case "user":
			buf.WriteString("[用户]\n")
			buf.WriteString(m.Content)
			buf.WriteString("\n\n")
		case "assistant":
			buf.WriteString("[助手]\n")
			buf.WriteString(m.Content)
			if len(m.ToolCalls) > 0 {
				buf.WriteString("\n[工具调用]")
				for _, tc := range m.ToolCalls {
					fmt.Fprintf(&buf, "\n- %s(%s)", tc.Name, tc.Arguments)
				}
			}
			buf.WriteString("\n\n")
		case "tool":
			buf.WriteString(fmt.Sprintf("[工具结果: %s]\n", m.ToolCallID))
			// 截断过长的工具结果
			if len(m.Content) > 2000 {
				buf.WriteString(m.Content[:2000])
				buf.WriteString("...(截断)")
			} else {
				buf.WriteString(m.Content)
			}
			buf.WriteString("\n\n")
		case "system":
			buf.WriteString("[系统]\n")
			buf.WriteString(m.Content)
			buf.WriteString("\n\n")
		default:
			buf.WriteString(fmt.Sprintf("[%s]\n", m.Role))
			buf.WriteString(m.Content)
			buf.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(buf.String())
}
