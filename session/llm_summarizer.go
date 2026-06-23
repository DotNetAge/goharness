package session

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
const defaultSystemPrompt = `You are a professional memory summarizer. Condense the following conversation into independent memory chunks.

Each chunk is a self-contained knowledge unit that must remain understandable and usable without the original conversation context.

Each chunk contains:
1. summary: A brief phrase or single short sentence capturing the gist (used only for semantic retrieval; keep it concise to maximize embedding precision)
2. content: Complete memory content (all key information of this unit, no extra compression; must be independently understandable without context)
3. tags: Keyword tags (at least 2 per chunk; must be distinctive for topic-based filtering)

Rules:
- Each chunk focuses on one topic, decision, or knowledge unit
- Only keep substantive information directly relevant to that topic (e.g., rationale, technical decisions, explicit user preferences)
- Do not fabricate; do not omit key decisions or conclusions
- Content length is determined by what fully covers the knowledge unit — no upper limit
- Output raw JSON array only — no markdown code blocks, no extra text
- Return an empty array if the conversation is trivial (e.g., casual greetings)
- The summary language must match the language used in the conversation

Output format (strict JSON array):
[
  {
    "summary": "Brief phrase or short sentence",
    "content": "Complete self-contained memory content",
    "tags": ["distinctive-tag-1", "distinctive-tag-2"]
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
// 保留原始角色、内容和工具调用信息，不进行额外格式化。
func (s *llmSummarizer) toLLMMessages(messages []Message) []gochatcore.Message {
	out := make([]gochatcore.Message, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case "user":
			out = append(out, gochatcore.NewUserMessage(m.Content))
		case "assistant":
			msg := gochatcore.Message{
				Role: gochatcore.RoleAssistant,
				Content: []gochatcore.ContentBlock{
					{Type: gochatcore.ContentTypeText, Text: m.Content},
				},
			}
			if len(m.ToolCalls) > 0 {
				tcs := make([]gochatcore.ToolCall, len(m.ToolCalls))
				for i, tc := range m.ToolCalls {
					tcs[i] = gochatcore.ToolCall{
						ID:        tc.ID,
						Name:      tc.Name,
						Arguments: tc.Arguments,
					}
				}
				msg.ToolCalls = tcs
			}
			out = append(out, msg)
		case "tool":
			msg := gochatcore.Message{
				Role: gochatcore.RoleTool,
				Content: []gochatcore.ContentBlock{
					{Type: gochatcore.ContentTypeText, Text: m.Content},
				},
				ToolCallID: m.ToolCallID,
			}
			out = append(out, msg)
		case "system":
			out = append(out, gochatcore.NewSystemMessage(m.Content))
		}
	}
	return out
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
	var rawChunks []struct {
		Summary string   `json:"summary"`
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
	}
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
		chunks = append(chunks, memory.MemoryChunk{
			Summary: rc.Summary,
			Content: rc.Content,
			Tags:    tags,
		})
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
