package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/logging"
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
// 要求 LLM 输出一篇 markdown 格式的压缩摘要文档：这是压缩的最终产物原文，
// 经 Session 的 SummarySink 直接持久化为记忆文件，无任何中间解析层。
// 关键设计：用高信息密度的分节结构重建 LLM 的输出长度锚点，配合正向"必须保留"清单，
// 抵抗过度简化倾向。
//
// 末尾追加"不要调用任何工具"的显式禁令：因 ToolChoice 保持与主对话一致的 "auto"
// （三家国内大模型文档都没明确说 ToolChoice 是否参与缓存前缀匹配，保守不动），
// 靠指令文本禁止 LLM 在压缩阶段调工具。该文本位于末条 user message（新增的、
// 不在缓存前缀里的部分），改动不影响缓存命中。
const compactionInstruction = `将上面对话浓缩为一篇 markdown 格式的执行快照文档，供后续会话恢复上下文。

输出格式（严格按以下分节，节名不可改）：
# <本次工作的核心目标，动词性短语，≤20字>

## 执行状态
<一两句话：已干了什么 + 干得怎么样，含完成度（已完成/进行中/阻塞/失败）>

## 关键决策与结果
- <每条一个决策：选了什么、为什么、关键结果>
- <多个相关决策可合并为一条，但不要跨主题合并>

## 重要细节
- <文件路径、函数名、配置项、版本号、报错信息与解法、约束规约，均原样保留>
- <路径必须写完整绝对路径，禁止简化为文件名或相对路径>

## 待办与未决
- <待办事项、未决问题、阻塞点；无则写"无">

必须保留：
- 用户的核心意图和最终目标
- 已确定的决策及其理由（为什么选 X 而非 Y）
- 报错信息及对应的解决方案
- 明确的待办事项、未决问题、阻塞点
- 重要的约束、规约、边界条件
- 时间节点、截止日期、依赖关系

必须省略：
- 问候、寒暄、确认词（"好的"、"明白了"）
- 失败的中间尝试（除非失败原因本身有参考价值）
- 被否决且不会重用的方案细节
- 重复出现的相同信息

规则：
- 只输出这篇 markdown 文档，无其他文本
- 禁止对话体、问句、问候、情绪、表情符号
- 矛盾讨论只保留最终方案及其理由，省略被否决方案的细节
- 无任何实质信息时输出 "# 无实质内容\n\n## 执行状态\n\n本段对话无需要保留的信息。"
- 不要调用任何工具，直接输出文档，不要产生 tool_call`

// retryInstruction — 重试时使用的精简指令，同样放在最后一条 user message。
// 即使精简也保留分节语义（目标/执行状态/决策/细节/待办）与核心约束
// （完成度/理由/路径/待办），避免重试时退化为过度简化。
// 末尾同样追加工具调用禁令。
const retryInstruction = `将以上全部对话浓缩为一篇 markdown 执行快照文档，按以下分节输出：
# 核心目标（≤20字动词短语）
## 执行状态（已干了什么+干得怎么样，含完成度）
## 关键决策与结果（- 每条：选了什么、为什么、结果）
## 重要细节（- 文件路径/配置/报错与解法，路径用完整绝对路径）
## 待办与未决（- 无则写"无"）
只输出该 markdown 文档，无其他文本。无实质信息时输出"# 无实质内容"。
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

// Compact 执行一次压缩（含一次重试），返回压缩摘要原文（markdown）。
//
// 压缩指令来源：Session 的 CompactionPrompt() 覆盖优先（应用层按 Agent 特点定制
// 摘要重点），为空时使用内置默认 compactionInstruction。
//
// 重试策略：两次调用共用同一 system/tools/messages 前缀构造，仅末尾 instruction 不同。
// 第二次请求能命中第一次已落盘的前缀缓存——这是把重试放在 Compactor 层的额外收益。
// 外部覆盖指令时重试沿用同一覆盖指令；仅内置指令时重试换用精简版 retryInstruction。
//
// 返回：
//   - 成功：非空摘要原文
//   - 失败：error（首次失败 + 重试也失败时返回合并错误）。
//     空响应同样走失败路径触发重试：LLM 未返回 content 时无法区分
//     「判定无实质信息」与「服务端异常」，宁可重试一次也不静默丢压缩。
func (c *compactor) Compact(ctx context.Context, s *session.Session, messages []session.Message) (string, error) {
	instruction := s.CompactionPrompt()
	if instruction == "" {
		instruction = compactionInstruction
	}

	// 首次尝试
	summary, err := c.compactionTurn(ctx, s, messages, instruction)
	if err == nil {
		return summary, nil
	}
	// ctx 已取消时不重试
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", err
	}
	// 重试：同一前缀，仅末尾 instruction 不同
	retry := instruction
	if instruction == compactionInstruction {
		retry = retryInstruction
	}
	c.logger.Warn("压缩首次失败，使用精简指令重试", "error", err, "session_id", s.ID())
	summary, retryErr := c.compactionTurn(ctx, s, messages, retry)
	if retryErr != nil {
		return "", fmt.Errorf("compactor: %w (retry also failed: %v)", err, retryErr)
	}
	return summary, nil
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
// 返回 LLM 响应文本原文（markdown 摘要文档），由调用方经 SummarySink 持久化。
func (c *compactor) compactionTurn(ctx context.Context, s *session.Session, messages []session.Message, instruction string) (string, error) {
	sid := s.ID()
	agentName := s.AgentName()

	// 1. system prompts —— 与主对话一致（Runtime.exec 中同样调用 prompt.BuildSystemPrompts）
	systemMsgs := c.rt.prompt.BuildSystemPrompts(sid, s)

	// 2. tools 数组 —— 与本会话的主对话请求逐字段一致（Runtime.exec 中同样调用
	//    buildAllToolDefinitions）。必须走 effectiveExcludeTools 而非裸 ExcludeToolsFor：
	//    子会话（Sponsor 非空）的 exec 请求屏蔽了多 Agent 协作工具，压缩请求若不
	//    同步屏蔽，tools 字段与主请求不一致会导致前缀缓存不命中。审计发现的契约违背。
	excludeTools := effectiveExcludeTools(c.rt, agentName, s)
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
		return "", fmt.Errorf("compactor stream open: %w", err)
	}

	// 6. 流式收集：content 为主，thinking/FinishReason/Usage 作为观测字段同步收集。
	//    这些字段是空响应排障的唯一线索（生产曾出现 4 分钟空响应且无任何定位信息）。
	var contentBuf strings.Builder
	var thinkingChars int
	var toolCallEvents int
	var streamErr error
	var finishReason string
	var doneUsage *gochatcore.Usage
	for stream.Next() {
		ev := stream.Event()
		switch ev.Type {
		case gochatcore.EventContent:
			contentBuf.WriteString(ev.Content)
		case gochatcore.EventThinking:
			// 思考型模型（如 glm-4.7-flash）的输出可能全部落在思考流，
			// 思考长度是判定「模型输出了但 content 通道为空」的关键证据
			thinkingChars += len(ev.Content)
		case gochatcore.EventError:
			streamErr = ev.Err
		case gochatcore.EventToolCall:
			// ToolChoice="auto" 下模型理论上可能调工具，但靠 instruction 已禁止。
			// 若仍收到 tool_call 事件，记录日志但不中断流（让 content 部分仍被收集）。
			toolCallEvents += len(ev.ToolCallDeltas)
			c.logger.Warn("压缩阶段收到意外的 tool_call 事件（instruction 已禁止）",
				"session_id", sid)
		case gochatcore.EventDone:
			finishReason = ev.FinishReason
			// done 事件可能自带 Usage：Next 循环退出后迟到的 usage 尾包
			// 会在 Close 的 drain 中被丢弃，此处快照作回退来源
			doneUsage = ev.Usage
		}
	}
	stream.Close()

	if streamErr != nil {
		return "", fmt.Errorf("compactor stream read: %w", streamErr)
	}

	content := contentBuf.String()

	// 流结束后记录完整观测信息：Usage 优先取尾包汇总，缺失时回退 done 事件快照
	streamUsage := stream.Usage()
	if streamUsage == nil {
		streamUsage = doneUsage
	}
	usageFields := []any{"session_id", sid, "finish_reason", finishReason}
	if streamUsage != nil {
		usageFields = append(usageFields,
			"prompt_tokens", streamUsage.PromptTokens,
			"completion_tokens", streamUsage.CompletionTokens)
	} else {
		// Usage 为 nil 本身就是异常信号：流未走完服务端用量回报即终止
		usageFields = append(usageFields, "prompt_tokens", "nil", "completion_tokens", "nil")
	}
	c.logger.Info("压缩流结束",
		append(usageFields,
			"content_chars", len(content),
			"thinking_chars", thinkingChars,
			"tool_call_events", toolCallEvents)...)

	if content == "" {
		// 空响应必须返回错误触发 Compact 的重试机制（同前缀仅换末尾指令，命中前缀缓存）。
		// 附带观测字段：finish_reason=length 指向输出预算被思考流耗尽；
		// usage 为 nil 指向服务端静默断流。
		return "", fmt.Errorf("压缩响应为空（finish_reason=%q, thinking_chars=%d, tool_call_events=%d）",
			finishReason, thinkingChars, toolCallEvents)
	}

	// 7. 原文直接返回：压缩摘要无解析层，content 即最终产物，
	//    由 Session 的 SummarySink 持久化为记忆文件。
	c.logger.Debug("压缩完成",
		"session_id", sid,
		"content_len", len(content))

	return content, nil
}
