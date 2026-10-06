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
// 要求 LLM 输出 JSON 数组：数组每一项是一条独立的会话记忆条目，落盘侧
// （mindx FileMemorySink）逐条写为一个记忆文件——单条粒度才能支撑复盘的
// 聚类合并、按条遗忘归档与独立检索。
//
// 质量锚点（本指令的核心）：记忆条目是将来蒸馏技能的原料，条目内容质量直接
// 决定技能质量。拆分原则教 LLM 判断"什么值得独立成条"；content 字段规范教它
// "怎么写才有蒸馏价值"（决策与理由、可复现路径、踩坑与解法原样保留）。
//
// 引文规范（软实现约定）：引用外部权威资料（公理库 references/）时只写提炼
// 结论加 [ref:条目ID] 标记，refs 字段结构化存放条目 ID——复盘可沿引文依赖
// 连锁校验推论有效性。正文与引用分离，防记忆膨胀、保知识溯源。
//
// 末尾追加"不要调用任何工具"的显式禁令：因 ToolChoice 保持与主对话一致的
// "auto"（三家国内大模型文档都没明确说 ToolChoice 是否参与缓存前缀匹配，
// 保守不动），靠指令文本禁止 LLM 在压缩阶段调工具。该文本位于末条 user
// message（新增的、不在缓存前缀里的部分），改动不影响缓存命中。
const compactionInstruction = `将上面对话压缩为 JSON 数组格式的会话记忆。数组每一项是一条独立的记忆条目——一个事件、一个决策、一条可复用经验或踩坑，将各自独立存放、独立检索，并作为复盘提炼技能的原料。条目内容质量直接决定技能质量，宁缺毋滥。

输出格式：
[
  {
    "kind": "条目类型：experience（可复用经验）/ decision（决策及理由）/ pitfall（踩坑与解法）/ event（事件记录）",
    "title": "这条记忆是什么（动词性短语，≤20字，独立可检索）",
    "summary": "一句话事件概括：做了什么 + 结果与完成度",
    "content": "完整细节：决策及其理由、可复现的执行路径、踩坑现象与解法、关键数据。路径/函数名/配置项/版本号原样保留，路径必须完整绝对路径，禁止简化为文件名或相对路径",
    "tags": ["3-5 个关键词，小写短横线分隔；第一个关键词作为该条记忆的分类"],
    "refs": ["对话中实际引用的公理条目 ID（如 ref_001）；未引用则为空数组"],
    "timestamp": "本条最重要事件的 ISO 8601 时间"
  }
]

拆分原则（决定记忆质量的核心）：
- 一条记忆只讲一个主题；多个相关决策可合并为一条，跨主题必须拆分
- 每条必须自包含：脱离对话上下文也能独立理解，禁止出现"上面/刚才/如前所述"等会话内指代
- 优先记录：决策及其理由、可复现的执行路径、踩坑现象与解法、报错与方案、约束规约、明确待办与阻塞点
- 有信息增量的才记：问候寒暄、确认词、被否决且不会重用的方案、无借鉴价值的失败尝试不记
- 禁止搬运工具原始输出与日志全文：工具结果只提炼有效结论与关键数据，原始输出留待需要时按文件路径重新读取
- 引用外部权威资料（论文、官方规范，存于 .agents/references/ 公理库）时，content 里只写提炼结论并在句末标注 [ref:条目ID]，禁止粘贴原文；对应条目 ID 写入 refs 字段；未引用权威资料时 refs 为空数组，不得虚构 [ref:*] 标记

规则：
- 只输出原始 JSON 数组，无其他文本
- 禁止对话体、问句、问候、情绪、表情符号
- 无任何实质信息时返回 []
- 不要调用任何工具，直接输出 JSON 数组，不要产生 tool_call`

// retryInstruction — 重试时使用的精简指令，同样放在最后一条 user message。
// 即使精简也保留拆分原则、字段职责与引文规范，避免重试时退化为过度简化。
// 末尾同样追加工具调用禁令。
const retryInstruction = `将以上全部对话压缩为 JSON 数组格式的会话记忆，每条一个独立主题（事件/决策/经验/踩坑），供复盘提炼技能。每条格式：
{"kind":"experience|decision|pitfall|event","title":"这条记忆是什么(≤20字动词短语)","summary":"一句话事件概括(做了什么+结果)","content":"完整细节(决策及理由、可复现路径、踩坑与解法,路径用完整绝对路径原样保留)","tags":["3-5个关键词,首个为分类"],"refs":["引用的公理条目ID,无则[]"],"timestamp":"ISO 8601"}
拆分原则：一条只讲一个主题，每条自包含（禁止"上面/刚才"等会话内指代）；只记有信息增量的内容；工具原始输出与日志全文禁止搬运，只留结论与关键数据；引用外部权威资料只写结论加 [ref:条目ID]，禁止粘贴原文；问候寒暄与无借鉴价值的过程不记。
只输出原始 JSON 数组，无其他文本。无实质信息时返回 []。
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
