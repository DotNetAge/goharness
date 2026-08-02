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
type llmSummarizer struct {
	model        config.ModelConfig
	getModel     func() config.ModelConfig
	systemPrompt string
}

// defaultSystemPrompt — 角色与核心原则设定，明确"信息密度优先于简洁"的压缩倾向，
// 抵消 LLM 在摘要任务中常见的过度简化倾向。格式指令放在最后一条 user message。
const defaultSystemPrompt = `你是会话上下文压缩器。任务是把多轮对话浓缩为结构化摘要 JSON 数组，供后续对话作为唯一上下文使用。

核心原则：信息密度优先于简洁。遗漏关键细节会导致后续对话失忆，过度简化比略微冗余更有害。
保留对话的"决策脉络"：不仅记结论，还要记理由、约束、待办，让后续对话能无缝继续。`

// summarizeInstruction — 放在最后一条 user message，居于注意力峰值位置。
// 采用 title/summary/content 三段式：字段名即字段职责，消除旧版 summary 字段名
// 与"标题"职责的语义冲突，让 LLM 输出更有结构导向性。
// 关键设计：用高信息密度的示例重建 LLM 的输出长度锚点，配合正向"必须保留"清单，
// 抵消示例过短导致的过度简化倾向。
const summarizeInstruction = `将上面对话浓缩为 JSON 数组格式的结构化摘要。每个主题一条，相关决策可合并为同一条。

输出格式：
[
  {
    "title": "主题标题（名词性短语，≤15字，用于检索召回）",
    "summary": "核心结论（一两句话，概括这条记忆的最终结论和最关键理由）",
    "content": "详细要点，用 - 分条列举，保留决策、理由、关键实体、待办",
    "tags": ["3-5个从内容提取的关键词，小写短横线分隔"],
    "timestamp": "本主题最重要事件的 ISO 8601 时间（如 2026-07-02T14:30:45Z）"
  }
]

三段式字段职责（严格区分，字段名即职责，不可混淆）：
- title：极短导航标题，名词性短语，不要写成完整句子
  正确："Redis 迁移 Cluster" / 错误："讨论了 Redis 的迁移问题"
- summary：一两句话核心结论，让读者快速判断这条记忆讲什么、结论是什么。包含"做了什么决定" + 最关键理由
  正确："决定从 Redis 单点迁移到 Cluster，因单点无法支撑 50K QPS"
- content：详细要点，分条列举。title 和 summary 是 content 的索引与概括，content 是完整细节

content 字段要求（决定压缩质量的关键）：
- 用 "- " 前缀分条，每条一个完整信息单元，用 \n 分隔
- 每条必须是有信息增量的完整陈述，不要写"讨论了 X"这种无内容的话
- 多个相关决策可合并到同一条 content，但不要跨主题合并
- 长度不限，但每条都要有信息增量，不要为凑长度重复
- 路径、标识符、配置项必须原样复制，不得缩写、省略、改写大小写

必须保留：
- 用户的核心意图和最终目标
- 已确定的决策及其理由（为什么选 X 而非 Y）
- 文件路径：必须原样复制完整绝对路径，禁止简化为文件名、禁止省略前缀、禁止改为相对路径（如 /Users/ray/.../semantic.go 不可写成 semantic.go 或 indexer/semantic.go）；函数名、API 名、配置项、版本号、数值参数同样原样保留
- 报错信息及对应的解决方案
- 明确的待办事项、未决问题、阻塞点
- 重要的约束、规约、边界条件
- 时间节点、截止日期、依赖关系

必须省略：
- 问候、寒暄、确认词（"好的"、"明白了"）
- 失败的中间尝试（除非失败原因本身有参考价值）
- 被否决且不会重用的方案细节
- 重复出现的相同信息

示例：
[
  {
    "title": "Redis 迁移 Cluster",
    "summary": "决定从 Redis 单点迁移到 Cluster 模式，Q3 完成，因单点已无法支撑 50K QPS。",
    "content": "- 决策：从 Redis 单点迁移到 Cluster，Q3 完成\n- 理由：单点已无法支撑 50K QPS，Cluster 可水平扩展\n- 关键路径：/etc/redis/redis-cluster.conf\n- 阻塞点：lettuce 客户端需升级到 6.x 才支持 Cluster\n- 待办：迁移方案需 DBA 评审后实施",
    "tags": ["redis", "cluster-migration", "lettuce"],
    "timestamp": "2026-07-02T14:30:45Z"
  },
  {
    "title": "前端构建切 Vite",
    "summary": "构建工具从 Webpack 5 切换到 Vite 5，解决冷启动慢问题，CI 脚本待同步调整。",
    "content": "- 决策：构建工具从 Webpack 5 切换到 Vite 5\n- 理由：Webpack 冷启动 90s，Vite 仅 2s，开发体验显著提升\n- 配置：vite.config.ts 保留原有 alias 配置\n- 兼容性：需保留 @vitejs/plugin-vue 5.x\n- 待办：CI 流水线 build 脚本需同步调整",
    "tags": ["vite", "webpack-migration", "frontend-build"],
    "timestamp": "2026-07-03T09:15:00Z"
  }
]

规则：
- 只输出原始 JSON 数组，无其他文本
- 禁止对话体、问句、问候、情绪、表情符号
- 单主题只输出一条；多主题拆成多条
- title 是名词短语，summary 是结论句，content 是分条细节，三者不可混淆或互相替代
- 矛盾讨论只保留最终方案及其理由，省略被否决方案的细节
- 待决问题独立成条并以"待办："标注，不要省略
- tags 从 content 提取
- 无任何实质信息时返回 []`

// retryInstruction — 重试时使用的精简指令，同样放在最后一条 user message。
// 即使精简也保留三段式结构与核心约束（决策/理由/路径/待办），避免重试时退化为过度简化。
const retryInstruction = `将以上全部对话输出为 JSON 数组，每个主题一条。
每条格式：{"title":"主题标题(≤15字名词短语)","summary":"核心结论(一两句话)","content":"- 要点1\n- 要点2(保留决策、理由、文件路径、待办)","tags":["标签"],"timestamp":"ISO 8601"}。
title 是名词短语，summary 是结论句，content 用 - 分条列举且必须保留：决策结论、理由、关键路径/函数名/参数、报错及解决方案、待办事项。
只输出 JSON 数组，无其他文本。无实质信息时返回 []。`

// summarizeTimeout 摘要请求的最大等待时长。
//
// 注意：gochat 的 WithTimeout(0) 会被 NewClient 当作"未设置"替换成默认 30 秒，
// 而摘要请求携带的对话上下文可能达 10 万级 token，本地小模型（如 ollama）
// 首响应可能远超 30 秒，导致压缩被 Client.Timeout 打断、cursor 不移动的"假压缩"。
// 因此必须显式传入足够长的超时。
const summarizeTimeout = 10 * time.Minute

// summarizeMaxRetries 摘要请求允许的 gochat 内层重试次数。
// 摘要请求本身耗时数分钟，内层重试会叠加指数退避成数十分钟无谓等待，
// 故关闭内层重试，仅保留 Summarize 外层基于精简指令的一次重试。
const summarizeMaxRetries = 0

// NewLLMSummarizer 创建一个新的 LLM 摘要器实例。
//
// 模型动态化：通过 WithModelResolver 注入回调后，每次摘要都会重新读取
// 当前模型配置（APIKey/BaseURL/Name），保证切换模型后摘要器立即生效。
// 回调返回空 ModelConfig 时回退到本构造函数传入的固定 model。
func NewLLMSummarizer(model config.ModelConfig, opts ...SummarizerOption) Summarizer {
	s := &llmSummarizer{
		model:        model,
		systemPrompt: defaultSystemPrompt,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// resolveModel 返回当前摘要应使用的模型配置。
//
// 模型来源优先级：getModel 回调 > 构造时传入的固定 model 字段。
//
// 这是「Summarizer 随模型切换更新」的核心：每次摘要调用都重新解析，
// 不再使用构造时固化的快照。
func (s *llmSummarizer) resolveModel() config.ModelConfig {
	m := s.model
	if s.getModel != nil {
		if resolved := s.getModel(); resolved.Name != "" {
			m = resolved
		}
	}
	return m
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
// 保证会话切换模型后摘要器立即使用新模型（APIKey/BaseURL/Name 同步）。
func (s *llmSummarizer) trySummarize(ctx context.Context, messages []Message, systemPrompt, instruction string) ([]memory.MemoryChunk, error) {
	condensed := s.formatMessages(messages)

	msgs := []gochatcore.Message{
		gochatcore.NewSystemMessage(systemPrompt),
		gochatcore.NewUserMessage(condensed),
		gochatcore.NewUserMessage(instruction),
	}

	// 动态解析当前模型，跟随模型切换
	model := s.resolveModel()

	resp, err := gochat.Client().
		Config(
			gochat.WithAPIKey(model.APIKey),
			gochat.WithBaseURL(model.BaseURL),
			gochat.WithTimeout(summarizeTimeout),
			gochat.WithMaxRetries(summarizeMaxRetries),
		).
		Messages(msgs...).
		Model(model.Name).
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

// rawChunk 是 parseResponse 解析 LLM 输出 JSON 时使用的中间结构体，采用三段式。
// Title/Summary/Content 三段各司其职：Title 是导航标题、Summary 是核心结论、Content 是分条细节。
// Timestamp 是字符串（LLM 输出 ISO 8601），需在 parseResponse 中解析为 time.Time。
// 如果 LLM 不填或解析失败，MemoryChunk.Timestamp 保持零值，由调用方（session.generateSummary）fallback。
type rawChunk struct {
	Title     string   `json:"title,omitempty"`
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

// buildChunkFromRaw 将 LLM 输出的 rawChunk 转为 MemoryChunk，映射三段式 Title/Summary/Content。
// Timestamp 解析 LLM 提供的 ISO 8601 字符串；解析失败保持零值，由上层 fallback。
func buildChunkFromRaw(rc rawChunk) memory.MemoryChunk {
	tags := rc.Tags
	if tags == nil {
		tags = []string{}
	}
	chunk := memory.MemoryChunk{
		Title:   rc.Title,
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
			buf.WriteString(tsPrefix)
			buf.WriteString("[用户]\n")
			buf.WriteString(m.Content)
			buf.WriteString("\n\n")
		case "assistant":
			buf.WriteString(tsPrefix)
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
			buf.WriteString(tsPrefix)
			fmt.Fprintf(&buf, "[工具结果: %s]\n", m.ToolCallID)
			// 截断过长的工具结果
			if len(m.Content) > 2000 {
				buf.WriteString(m.Content[:2000])
				buf.WriteString("...(截断)")
			} else {
				buf.WriteString(m.Content)
			}
			buf.WriteString("\n\n")
		case "system":
			buf.WriteString(tsPrefix)
			buf.WriteString("[系统]\n")
			buf.WriteString(m.Content)
			buf.WriteString("\n\n")
		default:
			buf.WriteString(tsPrefix)
			fmt.Fprintf(&buf, "[%s]\n", m.Role)
			buf.WriteString(m.Content)
			buf.WriteString("\n\n")
		}
	}
	return strings.TrimSpace(buf.String())
}
