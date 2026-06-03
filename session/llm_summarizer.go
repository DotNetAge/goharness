package session

import (
	"context"
	"fmt"
	"strings"

	gochat "github.com/DotNetAge/gochat"
	gochatcore "github.com/DotNetAge/gochat/core"

	"github.com/DotNetAge/goreact/config"
)

// SummarizerOption 是 LLMSummarizer 的构造选项函数类型
type SummarizerOption func(*llmSummarizer)

// WithSystemPrompt 设置摘要器的自定义系统提示词。
// 如果不设置，将使用默认的摘要提示词。
func WithSystemPrompt(prompt string) SummarizerOption {
	return func(s *llmSummarizer) { s.systemPrompt = prompt }
}

// WithMaxTokens 设置摘要输出的最大 token 数。
// 默认值为 1024。
func WithMaxTokens(n int) SummarizerOption {
	return func(s *llmSummarizer) { s.maxTokens = n }
}

// llmSummarizer 是基于 LLM 的独立摘要器实现，
// 通过调用 gochat 对输入内容进行摘要化处理。
type llmSummarizer struct {
	model        config.ModelConfig
	systemPrompt string
	maxTokens    int
}

// defaultSystemPrompt 是默认的摘要系统提示词
const defaultSystemPrompt = `你是一个专业的对话摘要助手。请对以下对话内容进行简洁、准确的摘要。

要求：
1. 保留关键信息、决策和结论
2. 省略冗余的细节和重复内容
3. 使用简洁的中文输出摘要
4. 摘要长度控制在原文的 30% 以内`

// NewLLMSummarizer 创建一个新的 LLM 摘要器实例。
//
// 参数：
//   - model: 必需的模型配置，包含 API 连接信息和模型名称
//   - opts: 可选的配置函数（WithSystemPrompt, WithMaxTokens 等）
//
// 返回：
//   - Summarizer: 实现了 Summarizer 接口的摘要器实例
//
// 示例：
//
//	summarizer := session.NewLLMSummarizer(config.ModelConfig{
//	    Name:    "gpt-4o-mini",
//	    APIKey:  "sk-xxx",
//	    BaseURL: "https://api.openai.com/v1",
//	}, session.WithSystemPrompt("请用英文摘要以下内容"))
//
//	summary, err := summarizer.Summarize(ctx, messages)
func NewLLMSummarizer(model config.ModelConfig, opts ...SummarizerOption) Summarizer {
	s := &llmSummarizer{
		model:        model,
		systemPrompt: defaultSystemPrompt,
		maxTokens:    1024,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Summarize 调用 LLM 对输入的消息列表进行摘要处理。
//
// 将消息转换为文本格式后，连同系统提示词一起发送给 LLM，
// 返回生成的摘要文本。
func (s *llmSummarizer) Summarize(ctx context.Context, messages []Message) (string, error) {
	if len(messages) == 0 {
		return "", nil
	}

	content := s.formatMessages(messages)
	if content == "" {
		return "", nil
	}

	resp, err := gochat.Client().
		Config(
			gochat.WithAPIKey(s.model.APIKey),
			gochat.WithBaseURL(s.model.BaseURL),
			gochat.WithTimeout(0), // 使用 context 控制超时
		).
		Messages(
			gochatcore.NewSystemMessage(s.systemPrompt),
			gochatcore.NewUserMessage(content),
		).
		Model(s.model.Name).
		MaxTokens(s.maxTokens).
		Temperature(0.3).
		WithContext(ctx).
		GetResponse()

	if err != nil {
		return "", fmt.Errorf("summarizer: %w", err)
	}

	if resp == nil || resp.Content == "" {
		return "", nil
	}

	return resp.Content, nil
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
