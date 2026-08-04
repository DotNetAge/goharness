package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/memory"
	"github.com/DotNetAge/goharness/session"
)

// compactionTimeout 压缩请求的最大等待时长。
//
// 注意：gochat 的 WithTimeout(0) 会被 NewClient 当作"未设置"替换成默认 30 秒，
// 而压缩请求携带的对话上下文可能达 10 万级 token，本地小模型（如 ollama）
// 首响应可能远超 30 秒，导致压缩被 Client.Timeout 打断、cursor 不移动的"假压缩"。
// 因此必须显式传入足够长的超时。
//
// 此值与主对话的 defaultLLMTimeout 不同——压缩响应通常比单轮对话长，
// 且压缩不在用户交互关键路径上，可以容忍更长等待。
const compactionTimeout = 10 * time.Minute

// compactionInstruction — 放在最后一条 user message，居于注意力峰值位置。
// 采用 title/summary/content 三段式：字段名即字段职责，消除旧版 summary 字段名
// 与"标题"职责的语义冲突，让 LLM 输出更有结构导向性。
// 关键设计：用高信息密度的示例重建 LLM 的输出长度锚点，配合正向"必须保留"清单，
// 抵消示例过短导致的过度简化倾向。
//
// 末尾追加"不要调用任何工具"的显式禁令：因 ToolChoice 保持与主对话一致的 "auto"
// （三家国内大模型文档都没明确说 ToolChoice 是否参与缓存前缀匹配，保守不动），
// 靠指令文本禁止 LLM 在压缩阶段调工具。该文本位于末条 user message（新增的、
// 不在缓存前缀里的部分），改动不影响缓存命中。
const compactionInstruction = `将上面对话浓缩为 JSON 数组格式的结构化摘要。每个主题一条，相关决策可合并为同一条。

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
- 无任何实质信息时返回 []
- 不要调用任何工具，直接输出 JSON 数组，不要产生 tool_call`

// retryInstruction — 重试时使用的精简指令，同样放在最后一条 user message。
// 即使精简也保留三段式结构与核心约束（决策/理由/路径/待办），避免重试时退化为过度简化。
// 末尾同样追加工具调用禁令。
const retryInstruction = `将以上全部对话输出为 JSON 数组，每个主题一条。
每条格式：{"title":"主题标题(≤15字名词短语)","summary":"核心结论(一两句话)","content":"- 要点1\n- 要点2(保留决策、理由、文件路径、待办)","tags":["标签"],"timestamp":"ISO 8601"}。
title 是名词短语，summary 是结论句，content 用 - 分条列举且必须保留：决策结论、理由、关键路径/函数名/参数、报错及解决方案、待办事项。
只输出 JSON 数组，无其他文本。无实质信息时返回 []。
不要调用任何工具，不要产生 tool_call。`

// compactor 实现 session.Compactor。
//
// 复用 Runtime 的 llmClient 和请求构造逻辑（PromptAssembler.BuildSystemPrompts /
// buildAllToolDefinitions / AssembleMessages），确保压缩请求与主对话请求
// 在 system + tools + messages 前缀上逐 token 完全一致——这是命中 KV 前缀缓存
// 的前提（DeepSeek/通义千问/豆包三家官方文档一致要求从第 0 个 token 起前缀完整匹配）。
//
// 唯一允许的差异：messages 末尾手动追加一条 user 压缩指令。
type compactor struct {
	rt     *Runtime
	logger logging.Logger
}

// NewCompactor 创建压缩器实例。
//
// rt 提供主对话的请求构造能力（PromptAssembler.BuildSystemPrompts/buildAllToolDefinitions/
// AssembleMessages/llmClient/model）。压缩响应的 JSON 解析由 Compactor 内部
// 的 parseCompactionResponse 完成，不依赖任何外部解析器。
func NewCompactor(rt *Runtime) session.Compactor {
	return &compactor{
		rt:     rt,
		logger: rt.logger,
	}
}

// Compact 执行一次压缩（含一次重试）。
//
// 重试策略：两次调用共用同一 system/tools/messages 前缀构造，仅末尾 instruction 不同。
// 第二次请求能命中第一次已落盘的前缀缓存——这是把重试放在 Compactor 层的额外收益。
//
// 返回：
//   - 成功：chunks（可能为空，表示 LLM 判定无实质信息）
//   - 失败：error（首次失败 + 重试也失败时返回合并错误）
func (c *compactor) Compact(ctx context.Context, s *session.Session, messages []session.Message) ([]memory.MemoryChunk, error) {
	// 首次尝试
	chunks, err := c.compactionTurn(ctx, s, messages, compactionInstruction)
	if err == nil {
		return chunks, nil
	}
	// ctx 已取消时不重试
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	// 重试：同一前缀，仅末尾 instruction 不同
	c.logger.Warn("压缩首次失败，使用精简指令重试", "error", err, "session_id", s.ID())
	chunks, retryErr := c.compactionTurn(ctx, s, messages, retryInstruction)
	if retryErr != nil {
		return nil, fmt.Errorf("compactor: %w (retry also failed: %v)", err, retryErr)
	}
	return chunks, nil
}

// compactionTurn 执行一次实际的 LLM 压缩调用。
//
// 请求构造契约（不可违反）——与主对话请求逐字段一致，仅末尾追加压缩指令：
//   - system prompt:  rt.prompt.BuildSystemPrompts(sid, s)
//   - tools 数组:     buildAllToolDefinitions(rt.toolReg, rt.prompt.AgentExcludeTools(agentName))
//   - messages 前缀:  原始 messages（不 formatMessages、不重排），经 AssembleMessages 构造
//   - 末尾追加:       gochatcore.NewUserMessage(instruction) 手动 append
//   - Model/Temp/...: rt.model.*（与主对话一致）
//   - ToolChoice:     "auto"（与主对话一致，靠指令文本禁止工具调用）
//   - Timeout:        compactionTimeout（10min，压缩响应长）
//
// 返回 LLM 响应文本经 parseCompactionResponse 解析后的 MemoryChunk 列表。
func (c *compactor) compactionTurn(ctx context.Context, s *session.Session, messages []session.Message, instruction string) ([]memory.MemoryChunk, error) {
	sid := s.ID()
	agentName := s.AgentName()

	// 1. system prompts —— 与主对话一致（Runtime.exec 中同样调用 prompt.BuildSystemPrompts）
	systemMsgs := c.rt.prompt.BuildSystemPrompts(sid, s)

	// 2. tools 数组 —— 与主对话一致（Runtime.exec 中同样调用 buildAllToolDefinitions）。
	//    buildAllToolDefinitions 注释明确："所有工具一次性注册，不在迭代间改变工具集，
	//    以保持前缀缓存稳定。"
	excludeTools := c.rt.prompt.AgentExcludeTools(agentName)
	toolDefs := buildAllToolDefinitions(c.rt.toolReg, excludeTools)

	// 3. messages 前缀 —— 使用 AssembleMessages 构造，question="" 不追加。
	//    保留原始消息结构（role/content/tool_calls/tool_result），不 formatMessages。
	//    AssembleMessages 内部会调用 stripOrphanedToolCalls 剔除孤立 tool_call，
	//    与 session.generateCompactionChunks 中的 sanitizeMessagesForLLM 幂等共存。
	msgs := AssembleMessages(systemMsgs, messages, "")

	// 4. 末尾追加压缩指令（唯一允许的差异）。
	//    手动 append 而非传给 AssembleMessages 的 question 参数，避开去重检查，
	//    保证 instruction 一定被追加到末尾。
	msgs = append(msgs, gochatcore.NewUserMessage(instruction))

	c.logger.Debug("压缩请求构造完成",
		"session_id", sid,
		"msg_count", len(msgs),
		"tool_count", len(toolDefs),
		"instruction_len", len(instruction))

	// 5. 流式调用 LLM —— 所有字段与主对话一致（Runtime.exec 中同样调用 llmClient.Stream），
	//    唯一差异：Timeout 用 compactionTimeout（10min）而非 defaultLLMTimeout。
	stream, err := c.rt.llmClient.Stream(ctx, LLMRequest{
		Messages:          msgs,
		Model:             c.rt.model.Name,
		Temperature:       c.rt.model.Temperature,
		TopP:              c.rt.model.TopP,
		TopK:              c.rt.model.TopK,
		RepetitionPenalty: c.rt.model.RepetitionPenalty,
		FrequencyPenalty:  c.rt.model.FrequencyPenalty,
		Tools:             toolDefs,
		ToolChoice:        "auto",
		Timeout:           compactionTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("compactor stream open: %w", err)
	}

	// 6. 流式收集：只收集 content，忽略 thinking/tool_call/done 事件。
	//    模式参考 collectStreamResponse（此处为精简版：compaction 无需事件转发与 reasoning）。
	var contentBuf strings.Builder
	var streamErr error
	for stream.Next() {
		ev := stream.Event()
		switch ev.Type {
		case gochatcore.EventContent:
			contentBuf.WriteString(ev.Content)
		case gochatcore.EventError:
			streamErr = ev.Err
		case gochatcore.EventToolCall:
			// ToolChoice="auto" 下模型理论上可能调工具，但靠 instruction 已禁止。
			// 若仍收到 tool_call 事件，记录日志但不中断流（让 content 部分仍被收集）。
			c.logger.Warn("压缩阶段收到意外的 tool_call 事件（instruction 已禁止）",
				"session_id", sid)
		case gochatcore.EventThinking, gochatcore.EventDone:
			// 忽略思考内容和完成事件
		}
	}
	stream.Close()

	if streamErr != nil {
		return nil, fmt.Errorf("compactor stream read: %w", streamErr)
	}

	content := contentBuf.String()
	if content == "" {
		// 空响应返回 nil,nil（LLM 未返回内容，无实质信息可压缩）
		c.logger.Info("压缩响应为空，LLM 未返回内容", "session_id", sid)
		return nil, nil
	}

	// 7. 解析 LLM 返回的 JSON 文本为 MemoryChunk（纯解析，无 LLM 调用）
	chunks, err := parseCompactionResponse(content)
	if err != nil {
		return nil, fmt.Errorf("compactor parse: %w", err)
	}

	c.logger.Debug("压缩完成",
		"session_id", sid,
		"content_len", len(content),
		"chunks", len(chunks))

	return chunks, nil
}
