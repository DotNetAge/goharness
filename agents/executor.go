package agents

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
)

// exec 是 Runtime 的核心思考循环（ReAct：推理 + 行动）。
//
// 每一轮循环：
//  1. 获取当前对话窗口
//  2. 运行 BeforeLLM hooks
//  3. 组装消息并调用 LLM 流式接口
//  4. 记录 token 使用
//  5. 运行 AfterLLM hooks
//  6. 持久化助手消息（content + reasoning + tool_calls）
//  7. 执行工具调用并持久化工具结果
//  8. 检测 AskUser / 权限 pending 等终止条件
//
// 终止条件：
//   - 上下文被取消
//   - Hook 中止循环
//   - LLM 返回错误
//   - LLM 没有调用工具（得到最终答案）
//   - 达到最大迭代次数
//   - 工具需要用户授权（permission_pending）
//   - AskUser 工具被调用（ask_user_pending）
//
// 注意：本方法不适合并发调用同一个 Runtime 实例，调用方需自行同步。
func (rt *Runtime) exec(b *AskBuilder) {
	ctx := b.ctx
	sid := b.session.ID()
	logger := rt.logger

	logger.Info("exec started", "session", sid, "agent", b.agentName, "model", rt.model.Name)

	loopStart := time.Now()
	defer func() {
		logger.Info("exec finished",
			"session", sid,
			"agent", b.agentName,
			"reason", b.resultTerminationReason,
			"iterations", b.resultIterations,
			"duration_ms", time.Since(loopStart).Milliseconds(),
			"answer_len", len(b.resultAnswer),
			"has_error", b.resultErr != nil,
		)
	}()

	// 事件总线
	eb := events.NewEventBus()
	if logger != nil {
		eb.SetLogger(logger)
	}
	_, cancel := eb.SubscribeFiltered(func(ev events.ReactEvent) bool {
		// 如果是子智能体，将事件转发到父级事件总线，
		// 以便客户端能够看到跨智能体边界的事件。
		if b.parentEmit != nil {
			logger.Debug("[exec] 转发事件到父级 EventBus",
				"type", ev.Type,
				"agent", ev.AgentName,
				"session", ev.SessionID,
			)
			b.parentEmit(ev)
		}
		// 先触发通用的 OnEvent 处理器，再触发类型特定的处理器。
		for _, h := range b.onAnyEvent {
			h(ev)
		}
		if handlers, ok := b.onEvent[ev.Type]; ok {
			for _, h := range handlers {
				h(ev.Data)
			}
		}
		return false
	})
	defer cancel()

	emit := func(typ events.ReactEventType, data any) {
		eb.Emit(events.ReactEvent{
			SessionID: sid,
			AgentName: b.agentName,
			Type:      typ,
			Data:      data,
			Timestamp: time.Now().UnixMilli(),
		})
	}
	// 将父级 EventBus 发射器存入 context，供子智能体（spawnSubAgent）获取并转发事件。
	ctx = context.WithValue(ctx, parentEmitKeyType{}, func(ev events.ReactEvent) {
		eb.Emit(ev)
	})
	conversationID := session.NewRecordID()
	var totalUsage = session.TokenUsage{Timestamp: time.Now()}
	defer func() {
		if rt.toolActivationHook != nil {
			rt.toolActivationHook.SetActiveToolSet(nil)
		}
		emit(events.ExecutionSummary, events.ExecutionSummaryData{
			TotalIterations:   b.resultIterations,
			TotalDuration:     b.resultDuration,
			TokensUsed:        totalUsage,
			TerminationReason: b.resultTerminationReason,
		})
	}()

	// 注册会话压缩事件处理器
	b.session.SetCompactionHandler(func(ev session.CompactionEvent) {
		emit(events.Compaction, events.CompactionData{
			SessionID:      sid,
			MessagesSlid:   ev.MessagesSlid,
			RemainingAfter: ev.RemainingAfter,
			WindowSize:     ev.WindowSize,
		})
	})

	// 压缩开始事件
	var compactBeforeTokens int64
	b.session.SetCompactStartHandler(func(windowTokens, maxWindowSize int64) {
		compactBeforeTokens = windowTokens
		emit(events.CompactStart, events.CompactStartData{
			SessionID:     sid,
			WindowTokens:  windowTokens,
			MaxWindowSize: maxWindowSize,
		})
	})

	// 压缩完成事件
	b.session.SetCompactDoneHandler(func(messagesSlid int, windowTokens int64) {
		var ratio float64
		if compactBeforeTokens > 0 {
			ratio = float64(windowTokens) / float64(compactBeforeTokens)
		}
		emit(events.CompactDone, events.CompactDoneData{
			SessionID:     sid,
			MessagesSlid:  messagesSlid,
			WindowTokens:  windowTokens,
			MaxWindowSize: b.session.MaxWindowSize(),
			Ratio:         ratio,
		})
	})

	// 微压缩开始事件
	var microCompactBeforeTokens int64
	b.session.SetMicroCompactStartHandler(func(windowTokens, maxWindowSize int64) {
		microCompactBeforeTokens = windowTokens
		emit(events.MicroCompactStart, events.MicroCompactStartData{
			SessionID:     sid,
			WindowTokens:  windowTokens,
			MaxWindowSize: maxWindowSize,
		})
	})

	// 微压缩完成事件
	b.session.SetMicroCompactDoneHandler(func(compressed, deduped int, windowTokens int64) {
		var ratio float64
		if microCompactBeforeTokens > 0 {
			ratio = float64(windowTokens) / float64(microCompactBeforeTokens)
		}
		emit(events.MicroCompactDone, events.MicroCompactDoneData{
			SessionID:     sid,
			Compressed:    compressed,
			Deduped:       deduped,
			WindowTokens:  windowTokens,
			MaxWindowSize: b.session.MaxWindowSize(),
			Ratio:         ratio,
		})
	})

	// 在创建工具执行器前预加载会话元数据，
	// 以便 WithProjectDirExecutor 从持久化的会话元数据中获取正确的项目目录。
	b.session.Current()

	// 偏移量模型（memmache.md）：在新一个轮次开始前检查活跃窗口 Token 是否超限，
	// 若超限则对 messages[cursor:] 全量摘要并清空（cursor = len(messages)）。
	// 不在 Append 末尾或 exec 循环中调用，避免工具结果 append 中途触发清空
	// 破坏 tool_call 配对。
	// Budget 动态可调：只有 maxWindowSize > 0（即模型 ContextLength <= 128K）时
	// 才会真正触发；超长上下文模型不设置 maxWindowSize，TryCompact 直接返回。
	b.session.TryCompact(ctx)

	// 从此刻开始计时，用于后续结果统计。
	start := time.Now()

	// setIterResult 统一设置当前轮次的结果字段。
	setIterResult := func(iter int) {
		b.resultIterations = iter + 1
		b.resultDuration = time.Since(start)
		b.resultUsage = totalUsage
	}

	// appendAndAbort 将消息追加到会话；失败时记录错误、发送 Error 事件并终止循环。
	appendAndAbort := func(iter int, msg session.Message, desc string) bool {
		if err := b.session.Append(ctx, msg); err != nil {
			logger.Error("追加消息失败", err, "session", sid, "desc", desc)
			emit(events.Error, fmt.Sprintf("追加%s失败: %v", desc, err))
			b.resultErr = fmt.Errorf("追加%s失败: %w", desc, err)
			b.resultTerminationReason = "error"
			setIterResult(iter)
			return false
		}
		return true
	}

	// 工具执行器 —— Session 指针是会话级状态的权威来源
	//（ID、ProjectDir、AgentName 等）。工具通过 Session 的 getter 访问这些属性，
	// 而不是使用提取出来的副本。
	//
	// 权限流程：执行器本身不再携带 ToolPermissionChecker。
	// 权限 enforcement 现在是工具内部的关注点（参见 tools.PermissionRequired / Grant）。
	// Runtime 在这里预先检查 Grant()；被拒绝的工具在 runtime 层就被拦下，不会进入执行器。
	toolExec := tools.NewToolExecutor(rt.toolReg,
		tools.WithEventEmitter(func(ev events.ReactEvent) { eb.Emit(ev) }),
		tools.WithLogger(logger),
		tools.WithSession(b.session),
		tools.WithSessionStore(rt.sessionStore),
		tools.WithKVStore(rt.kvStore),
	)

	maxIter := rt.model.MaxTurns
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}

	// ── 权限魔法词解析 ──
	// 如果用户消息是 "PermissionAllow" / "PermissionDeny" 且会话中存在上一轮留下的待处理授权，
	// 则在这里解析：执行工具（Allow）或合成拒绝结果（Deny）。用户魔法词本身不会被追加到会话，
	// 大模型只能看到工具结果。
	//
	// 这一步发生在追加用户消息之前，这样：
	//   - Allow 时，工具结果消息（使用原始 tool_call_id）已经存在于会话中，供下一次 LLM 调用。
	//   - 会话中 "assistant with tool_call" 与 "tool" 消息成对，满足 OpenAI 严格校验。
	magicHandled := rt.resolvePermissionMagicWord(ctx, b, toolExec, emit, logger)

	// 将用户消息追加到会话（仅当它不是被消费的魔法词时）。
	if !magicHandled {
		if !appendAndAbort(-1, session.Message{
			Role: "user", Content: b.question, Timestamp: time.Now().Unix(),
		}, "用户消息") {
			return
		}
	}
	// 如果魔法词已被消费，清空 b.question，避免下方循环将其作为普通用户消息重新注入 LLM 调用。
	if magicHandled {
		b.question = ""
	}

	// 判断当前智能体是否配置了 SubAgent，用于条件性地包含核心工具。
	hasSubAgent := false
	if rt.agentReg != nil {
		if cfg := rt.agentReg.Get(b.agentName); cfg != nil {
			excluded := make(map[string]bool, len(cfg.ExcludeTools))
			for _, name := range cfg.ExcludeTools {
				excluded[name] = true
			}
			hasSubAgent = !excluded["SubAgent"]
		}
	}

	// 创建本轮的 ActiveToolSet，并绑定到 ToolActivationHook。
	activeToolSet := NewActiveToolSet(rt.toolReg, hasSubAgent)
	if rt.toolActivationHook != nil {
		rt.toolActivationHook.SetActiveToolSet(activeToolSet)
	}
	// Reset 会在开始时重置为核心 + 条件性工具。
	activeToolSet.Reset()

	// 构建系统提示词段落（每轮之间静态不变）
	systemSections := rt.buildSystemPrompts(sid, b.session)

	var lastIteration int
	var prevToolResults []hooks.ToolResult

	for iter := 0; iter < maxIter; iter++ {
		if err := ctx.Err(); err != nil {
			b.resultErr = err
			b.resultTerminationReason = "cancelled"
			setIterResult(iter)
			return
		}

		// 获取当前对话窗口
		window := b.session.Current()
		logger.Info("session.Current() window", "session", sid, "iter", iter, "msg_count", len(window), "total_chars", func() int {
			total := 0
			for _, m := range window {
				total += len(m.Content)
			}
			return total
		}())

		// 从当前 ActiveToolSet 构建工具定义。
		// 不同轮次之间可能因 ToolSelector 或 Skill 激活新工具而发生变化。
		toolDefs := activeToolSet.BuildDefinitions()

		// ── Loop Hooks BeforeLLM（带 panic 恢复）──
		callInput := &hooks.CallInput{
			SessionID:            sid,
			AgentName:            b.agentName,
			ProjectDir:           b.session.ProjectDir(),
			SystemPromptSections: systemSections,
			UserMessage:          b.question,
			History:              window,
			Tools:                toolDefs,
		}
		if hr := rt.execBeforeLLMHooks(sid, iter, callInput, emit); hr.IsTerminal() {
			if hr.Error != nil {
				b.resultErr = hr.Error
				b.resultTerminationReason = "hook_error"
				setIterResult(iter)
				return
			}
			b.resultAnswer = hr.AbortReason
			b.resultTerminationReason = "hook_abort"
			setIterResult(iter)
			logger.Info("循环被钩子函数中止", "reason", hr.AbortReason)
			return
		}

		// 组装消息（Hook 可能修改 systemSections，例如 MemoryThoughtHook）
		windowHasQuestion := false
		for _, m := range window {
			if m.Role == "user" && m.Content == b.question {
				windowHasQuestion = true
				break
			}
		}
		question := ""
		if !windowHasQuestion {
			question = b.question
		}

		// MicroCompact（Dupdu）已禁用 —— 它修改上下文中间的 tool 消息内容，
		// 会破坏从首次请求累积下来的 KV 缓存，导致修改点之后的所有 KV 失效
		// 并重新计算。对于超长上下文模型，重算成本远大于保留"垃圾"的 attention
		// 成本；对于短上下文模型，TryCompact 会清空当前窗口，已足够控制长度。

		// 重新读取窗口 —— Current() 返回 messages[cursor:] 的新副本。
		window = b.session.Current()

		msgs := rt.assembleMessages(callInput.SystemPromptSections, window, question)

		// ── 调试：打印完整系统提示词 ──
		if rt.logger != nil {
			var sysTexts []string
			for _, m := range msgs {
				if m.Role == "system" {
					for _, block := range m.Content {
						if block.Type == "text" {
							sysTexts = append(sysTexts, block.Text)
						}
					}
				}
			}
			if len(sysTexts) > 0 {
				rt.logger.Debug("===== SYSTEM PROMPT =====",
					"session_id", sid, "iter", iter, "system_prompt", strings.Join(sysTexts, "\n---\n"))
			}
		}

		// ── 流式调用 LLM ──
		logger.Info("LLM思想流开始", "session", sid, "iter", iter)

		stream, err := rt.llmClient.Stream(ctx, LLMRequest{
			Messages:          msgs,
			Model:             rt.model.Name,
			Temperature:       rt.model.Temperature,
			MaxTokens:         rt.model.MaxTokens,
			TopP:              rt.model.TopP,
			TopK:              rt.model.TopK,
			RepetitionPenalty: rt.model.RepetitionPenalty,
			FrequencyPenalty:  rt.model.FrequencyPenalty,
			Tools:             toolDefs,
			ToolChoice:        "auto",
			Timeout:           defaultLLMTimeout,
		})
		if err != nil {
			logger.Error("LLM思想流失败", err, "session", sid, "iter", iter)
			emit(events.LLMTimeout, events.LLMTimeoutData{
				SessionID: sid,
				Timeout:   defaultLLMTimeout,
				Elapsed:   time.Since(start),
				Error:     err.Error(),
			})
			b.resultErr = fmt.Errorf("LLM思想流失败: %w", err)
			b.resultTerminationReason = "llm_error"
			setIterResult(iter)
			return
		}

		// 流式读取循环
		var contentBuf, reasoningBuf strings.Builder
		var streamToolCalls []gochatcore.ToolCall
		var finishReason string
		var streamErr error

		for stream.Next() {
			ev := stream.Event()
			switch ev.Type {
			case gochatcore.EventContent:
				contentBuf.WriteString(ev.Content)
				emit(events.ContentDelta, ev.Content)
			case gochatcore.EventThinking:
				reasoningBuf.WriteString(ev.Content)
				emit(events.ThinkingDelta, ev.Content)
			case gochatcore.EventToolCall:
				for _, delta := range ev.ToolCallDeltas {
					emit(events.ToolUseDelta, events.ToolUseDeltaData{
						Index:     delta.Index,
						ID:        delta.ID,
						Name:      delta.Name,
						Arguments: delta.Arguments,
					})
				}
			case gochatcore.EventError:
				streamErr = ev.Err
			case gochatcore.EventDone:
				finishReason = ev.FinishReason
			}
		}
		stream.Close()

		if streamErr != nil {
			emit(events.LLMTimeout, events.LLMTimeoutData{
				SessionID: sid,
				Timeout:   defaultLLMTimeout,
				Elapsed:   time.Since(start),
				Error:     streamErr.Error(),
			})
			b.resultErr = streamErr
			b.resultTerminationReason = "llm_error"
			setIterResult(iter)
			return
		}

		// 收集流式调用中的工具调用
		streamToolCalls = stream.ToolCalls()

		emit(events.ThinkingDone, nil)

		// 记录 token 使用情况
		callUsage := rt.recordStreamUsage(ctx, sid, b.agentName, iter, stream, start, conversationID, &totalUsage, emit, logger)

		// 根据流式数据构造 LLM 响应，供 hooks 使用
		content := contentBuf.String()
		reasoning := reasoningBuf.String()
		usageCopy := totalUsage
		llmResp := &hooks.LLMResponse{
			Content:      content,
			Reasoning:    reasoning,
			FinishReason: finishReason,
			ToolCalls:    parseToolInvocations(streamToolCalls),
			TokenUsage:   &usageCopy,
		}

		// ── Loop Hooks AfterLLM（带 panic 恢复）──
		if hr := rt.execAfterLLMHooks(sid, iter, llmResp, prevToolResults, emit); hr.IsTerminal() {
			if hr.Error != nil {
				b.resultErr = hr.Error
				b.resultTerminationReason = "hook_error"
				setIterResult(iter)
				return
			}
			llmResp.AbortReason = hr.AbortReason
		}

		// 处理 AfterLLM hook 的中止
		if llmResp.AbortReason != "" {
			emit(events.FinalAnswer, llmResp.Content)
			emit(events.TaskSummary, events.TaskSummaryData{
				Summary:    llmResp.Content,
				TokenUsage: totalUsage,
			})
			b.resultAnswer = llmResp.Content
			b.resultTerminationReason = "hook_abort"
			setIterResult(iter)
			return
		}

		// 持久化助手消息（content + reasoning + tool_calls）
		//
		// 某些 LLM 提供商（DeepSeek、Qwen、本地 vLLM 等）可能在流式响应中emit工具调用但不带 tool_call_id。
		// OpenAI 严格的消息格式要求助手消息中的每个 tool_call 后面必须跟着一条 tool_call_id 匹配的 tool 消息，
		// 否则会返回 400 "insufficient tool messages following tool_calls message"。
		//
		// 为了保持 assistant.ToolCalls 与后续持久化的 tool.ToolCallID 同步，
		// 我们为任何缺少 ID 的工具调用回填一个合成 ID。该 ID 随后被 parseToolInvocations 和 executeTools 使用，
		// 因此 pair 始终匹配。
		assistantMsg := session.Message{
			Role:             "assistant",
			Content:          content,
			ReasoningContent: reasoningBuf.String(),
			Timestamp:        time.Now().Unix(),
			Usage:            callUsage,
		}
		for i := range streamToolCalls {
			if streamToolCalls[i].ID == "" {
				streamToolCalls[i].ID = "syn_" + session.NewRecordID()
				logger.Warn("LLM调用了工具但没有包含ID，已填充合成的工具ID",
					"session", sid, "iter", iter, "tool", streamToolCalls[i].Name, "id", streamToolCalls[i].ID)
			}
			assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, session.ToolCall{
				ID:        streamToolCalls[i].ID,
				Name:      streamToolCalls[i].Name,
				Arguments: streamToolCalls[i].Arguments,
			})
		}
		if !appendAndAbort(iter, assistantMsg, "助手消息") {
			return
		}

		lastIteration = iter + 1
		emit(events.CycleEnd, events.CycleInfo{
			Iteration: lastIteration, Duration: time.Since(start),
		})

		// ── 没有工具调用 → 回答完成 ──
		if len(streamToolCalls) == 0 {
			answer := content
			if answer == "" && reasoning != "" {
				answer = reasoning
			}
			switch finishReason {
			case "length", "max_tokens":
				b.resultTerminationReason = "max_tokens"
			case "content_filter":
				b.resultTerminationReason = "content_filtered"
			default:
				b.resultTerminationReason = "completed"
			}
			emit(events.FinalAnswer, answer)
			emit(events.TaskSummary, events.TaskSummaryData{
				Summary:    answer,
				TokenUsage: totalUsage,
			})
			b.resultAnswer = answer
			b.resultIterations = lastIteration
			b.resultDuration = time.Since(start)
			b.resultUsage = totalUsage
			return
		}

		// ── 执行工具（带 hooks）──
		invocs := parseToolInvocations(streamToolCalls)

		// ── 执行前授权检查（PermissionRequired）──
		// 选择加入权限流程的工具实现 PermissionRequired.Grant()。
		// 对本轮的每个调用，我们询问工具"能否直接运行？"—— 若有工具回答否，则：
		//   1. 将调用保存到 session.PendingPermission（以便后续魔法词解析），
		//   2. 发送 PermissionPending 事件供 UI 渲染允许/拒绝对话框，
		//   3. 停止循环。工具不会被执行；助手的 tool_call 消息已持久化到会话（上面已完成），
		//      但对应的 tool 消息尚未添加。下一次 Ask() 调用 —— 当用户回复 PermissionAllow
		//      或 PermissionDeny 后 —— 会追加结果，从而满足 OpenAI 严格的 tool_call/tool-message 配对。
		//
		// 因为我们不会为被拒绝的工具调用 toolExec.Execute，所以大模型永远不会在上下文中看到
		// "需要授权" 的占位符。它只能看到最终结果（Allow 则成功，Deny 则"Permission Denied"），
		// 因此权限流程对大模型是不可见的。
		if pending := rt.checkPermissionGrants(ctx, b, invocs, emit, logger); pending != nil {
			emit(events.TaskSummary, events.TaskSummaryData{
				Summary:    fmt.Sprintf("请求授权执行工具: %s，等待用户批准...", pending.ToolName),
				TokenUsage: totalUsage,
			})
			b.resultTerminationReason = "permission_pending"
			setIterResult(iter)
			logger.Info("循环暂停: 工具需要授权，等待用户批准", "tool", pending.ToolName)
			return
		}

		executed := make(map[string]struct{}, len(invocs))
		for _, inv := range invocs {
			executed[inv.ID] = struct{}{}
		}

		toolResults := rt.executeTools(ctx, sid, invocs, emit, toolExec)
		prevToolResults = toolResults

		// 持久化工具结果（完整内容，不做截断）
		for _, tr := range toolResults {
			toolContent := formatToolResult(tr)
			preview := toolContent
			if len(preview) > 120 {
				preview = preview[:120]
			}
			logger.Info("工具结果已持久化", "session", sid, "tool", tr.ToolName, "content_len", len(toolContent), "content_preview", preview)
			if !appendAndAbort(iter, session.Message{
				Role: "tool", Content: toolContent, Timestamp: time.Now().Unix(),
				ToolCallID: tr.ToolCallID,
			}, "工具结果") {
				return
			}
		}

		// 为助手消息声明但未被执行的工具调用回填"跳过"结果。
		// 例如空 Name 或未找到的工具。没有这一步，assistant.ToolCalls 列表与 tool 消息数量不一致，
		// 下一次 LLM 请求会失败于 OpenAI 的 "insufficient tool messages" 错误。
		for _, tc := range streamToolCalls {
			if _, ok := executed[tc.ID]; ok {
				continue
			}
			name := tc.Name
			if name == "" {
				name = "<unknown>"
			}
			skippedContent := fmt.Sprintf("[%s]是个未知的工具，跳过执行: (id=%s)", name, tc.ID)
			logger.Warn("跳过执行工具",
				"session", sid, "tool", name, "tool_call_id", tc.ID)
			if !appendAndAbort(iter, session.Message{
				Role: "tool", Content: skippedContent, Timestamp: time.Now().Unix(),
				ToolCallID: tc.ID,
			}, "跳过工具结果") {
				return
			}
		}

		// ── 非阻塞 AskUser 检测 ──
		// 工具执行后检查是否调用了 AskUser。如果是，发送 AskUserPending 事件并退出循环。
		// 用户的回答将作为新会话轮次中的普通用户消息到达。
		if askUserInv := findAskUserInvocation(invocs); askUserInv != nil {
			askUserData := buildAskUserPendingData(askUserInv.Arguments)
			emit(events.AskUserPending, askUserData)
			emit(events.TaskSummary, events.TaskSummaryData{
				Summary:    "向用户提出问题，等待回答中...",
				TokenUsage: totalUsage,
			})
			b.resultTerminationReason = "ask_user_pending"
			setIterResult(iter)
			logger.Info("loop paused: AskUser tool invoked, waiting for user response")
			return
		}
	}

	// 达到最大迭代次数 —— 发送事件（不是错误）
	emit(events.MaxTurnsReached, events.MaxTurnsReachedData{
		TurnsCompleted: lastIteration,
		MaxTurns:       maxIter,
		Suggestion: fmt.Sprintf(
			"已达到最大思考轮次 (%d/%d)。任务可能需要更详细的指令或分步完成。你可以发送\"继续\"让 AI 基于当前进度继续，或者提供更具体的指导。",
			lastIteration, maxIter),
	})
	b.resultTerminationReason = "max_iterations"
	b.resultIterations = lastIteration
	b.resultDuration = time.Since(start)
	b.resultUsage = totalUsage
}

// formatToolResult 将 ToolResult 格式化为要持久化到会话的字符串。
func formatToolResult(tr hooks.ToolResult) string {
	if tr.Error != "" {
		return fmt.Sprintf("[%s] 执行错误: %s", tr.ToolName, tr.Error)
	}
	if tr.Result != "" {
		return tr.Result
	}
	return fmt.Sprintf("[%s] 返回结果: (空结果)", tr.ToolName)
}

// recordStreamUsage 从流式响应中提取 usage 并持久化到 TokenUsageStore。
// 如果 provider 没有返回 usage，则基于内容长度做保守估算。
func (rt *Runtime) recordStreamUsage(
	ctx context.Context,
	sid string,
	agentName string,
	iter int,
	stream *gochatcore.Stream,
	start time.Time,
	conversationID string,
	totalUsage *session.TokenUsage,
	emit func(events.ReactEventType, any),
	logger logging.Logger,
) *session.TokenUsage {
	u := stream.Usage()
	if u == nil {
		return rt.recordEstimatedUsage(ctx, sid, agentName, iter, stream, start, conversationID, totalUsage, emit, logger)
	}

	logger.Debug("stream.Usage() 返回非空",
		"session", sid, "iter", iter,
		"prompt_tokens", u.PromptTokens,
		"completion_tokens", u.CompletionTokens,
		"total_tokens", u.TotalTokens)

	callUsage := session.TokenUsage{
		Timestamp:        time.Now(),
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
	}
	if u.PromptTokensDetails != nil {
		callUsage.CachedTokens = u.PromptTokensDetails.CachedTokens
	}
	if u.CompletionTokensDetails != nil {
		callUsage.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}

	record := session.TokenUsageRecord{
		ID:               session.NewRecordID(),
		SessionID:        sid,
		ConversationID:   conversationID,
		ModelName:        rt.model.Name,
		ProviderName:     rt.model.Provider,
		AgentName:        agentName,
		PromptTokens:     callUsage.PromptTokens,
		CompletionTokens: callUsage.CompletionTokens,
		CachedTokens:     callUsage.CachedTokens,
		ReasoningTokens:  callUsage.ReasoningTokens,
		TotalTokens:      callUsage.TotalTokens,
		Timestamp:        time.Now(),
	}
	if err := rt.tokenUsageStore.Append(ctx, record); err != nil {
		logger.Error("添加词元使用记录失败", err, "session", sid)
	} else {
		logger.Debug("词元使用记录已持久化",
			"session", sid,
			"prompt_tokens", callUsage.PromptTokens,
			"completion_tokens", callUsage.CompletionTokens,
			"total_tokens", callUsage.TotalTokens,
			"cached_tokens", callUsage.CachedTokens)
		emit(events.TokenUsageRecorded, record)
	}
	totalUsage.PromptTokens += callUsage.PromptTokens
	totalUsage.CompletionTokens += callUsage.CompletionTokens
	totalUsage.TotalTokens += callUsage.TotalTokens
	totalUsage.CachedTokens += callUsage.CachedTokens
	totalUsage.ReasoningTokens += callUsage.ReasoningTokens
	totalUsage.Timestamp = time.Now()
	logger.Info("记录词元使用信息", "session", sid, "iter", iter, "input", u.PromptTokens, "output", u.CompletionTokens)
	return &callUsage
}

// recordEstimatedUsage 在 provider 未返回 usage 时基于内容长度做保守估算。
func (rt *Runtime) recordEstimatedUsage(
	ctx context.Context,
	sid string,
	agentName string,
	iter int,
	stream *gochatcore.Stream,
	start time.Time,
	conversationID string,
	totalUsage *session.TokenUsage,
	emit func(events.ReactEventType, any),
	logger logging.Logger,
) *session.TokenUsage {
	contentLen := 0
	reasoningLen := 0
	for stream.Next() {
		ev := stream.Event()
		switch ev.Type {
		case gochatcore.EventContent:
			contentLen += len(ev.Content)
		case gochatcore.EventThinking:
			reasoningLen += len(ev.Content)
		}
	}
	stream.Close()

	// 输入侧包含整个上下文窗口，无法从当前响应内容推导，故不重复计入输出。
	estimatedOutput := (contentLen + reasoningLen) / 4
	if estimatedOutput < 1 {
		estimatedOutput = 1
	}

	logger.Warn("stream.Usage() 返回 nil — 使用估算 token",
		"session", sid, "iter", iter, "model", rt.model.Name,
		"estimated_output", estimatedOutput)

	callUsage := session.TokenUsage{
		Timestamp:        time.Now(),
		PromptTokens:     0,
		CompletionTokens: estimatedOutput,
		TotalTokens:      estimatedOutput,
		ReasoningTokens:  reasoningLen / 4,
	}

	record := session.TokenUsageRecord{
		ID:               session.NewRecordID(),
		SessionID:        sid,
		ConversationID:   conversationID,
		ModelName:        rt.model.Name,
		ProviderName:     rt.model.Provider,
		AgentName:        agentName,
		PromptTokens:     0,
		CompletionTokens: callUsage.CompletionTokens,
		CachedTokens:     0,
		ReasoningTokens:  callUsage.ReasoningTokens,
		TotalTokens:      callUsage.TotalTokens,
		Timestamp:        time.Now(),
	}
	if err := rt.tokenUsageStore.Append(ctx, record); err != nil {
		logger.Error("添加词元使用估算记录失败", err, "session", sid)
	} else {
		emit(events.TokenUsageRecorded, record)
	}
	totalUsage.CompletionTokens += callUsage.CompletionTokens
	totalUsage.TotalTokens += callUsage.TotalTokens
	totalUsage.ReasoningTokens += callUsage.ReasoningTokens
	totalUsage.Timestamp = time.Now()
	return &callUsage
}

// execBeforeLLMHooks 按顺序执行所有注册的 LoopHook.BeforeLLM 方法。
// 任一 hook 返回 terminal 的 HookResult 时立即中止循环。
// 包含 panic 恢复，防止单个坏 hook 拖垮整个循环。
func (rt *Runtime) execBeforeLLMHooks(sid string, iter int, input *hooks.CallInput, emit func(events.ReactEventType, any)) (result hooks.HookResult) {
	defer func() {
		if p := recover(); p != nil {
			rt.logger.Error("loop hook panic", fmt.Errorf("%v", p))
			emit(events.Error, fmt.Sprintf("loop hook panic: %v", p))
			result = hooks.HookResult{Error: fmt.Errorf("loop hook panic: %v", p)}
		}
	}()
	for _, h := range rt.loopHooks {
		hr := h.BeforeLLM(sid, iter, input)
		if hr.IsTerminal() {
			rt.notifyLoopAbort(sid, hr.AbortReason)
			return hr
		}
	}
	return hooks.HookResult{}
}

// execAfterLLMHooks 按顺序执行所有注册的 LoopHook.AfterLLM 方法。
// 任一 hook 返回 terminal 的 HookResult 时立即中止循环。
// 包含 panic 恢复，防止单个坏 hook 拖垮整个循环。
func (rt *Runtime) execAfterLLMHooks(sid string, iter int, resp *hooks.LLMResponse, toolResults []hooks.ToolResult, emit func(events.ReactEventType, any)) (result hooks.HookResult) {
	defer func() {
		if p := recover(); p != nil {
			rt.logger.Error("loop hook panic", fmt.Errorf("%v", p))
			emit(events.Error, fmt.Sprintf("loop hook panic: %v", p))
			result = hooks.HookResult{Error: fmt.Errorf("loop hook panic: %v", p)}
		}
	}()
	for _, h := range rt.loopHooks {
		hr := h.AfterLLM(sid, iter, resp, toolResults)
		if hr.IsTerminal() {
			rt.notifyLoopAbort(sid, hr.AbortReason)
			return hr
		}
	}
	return hooks.HookResult{}
}

// notifyLoopAbort 通知所有已注册的 hook 循环正在中止。
// 按注册顺序的逆序（LIFO）调用 Abort()，让 hook 按与设置相反的顺序清理。
func (rt *Runtime) notifyLoopAbort(sessionID string, reason string) {
	for i := len(rt.loopHooks) - 1; i >= 0; i-- {
		rt.loopHooks[i].Abort(sessionID, reason)
	}
}

// executeTools 批量运行工具调用，支持同步（顺序）和异步（并发）两种模式。
//
// 执行策略：
//   - IsAsync == true：使用 goroutine 并发执行，超时为 asyncTimeout
//   - IsAsync == false：同步顺序执行，超时为 syncTimeout
//
// 每个工具执行都会经过 ToolHook 链：
//  1. Before 链（可中止/拒绝）
//  2. 实际执行 ToolExecutor.Execute
//  3. After 链（可修改结果）
func (rt *Runtime) executeTools(
	ctx context.Context,
	sessionID string,
	invocs []hooks.ToolCallInvocation,
	emit func(events.ReactEventType, any),
	toolExec tools.ToolExecutor,
) []hooks.ToolResult {
	if len(invocs) == 0 {
		return nil
	}

	results := make([]hooks.ToolResult, len(invocs))
	var wg sync.WaitGroup
	var mu sync.Mutex

	for i, inv := range invocs {
		if rt.isToolAsync(inv.Name) {
			wg.Add(1)
			i, inv := i, inv
			go func() {
				defer wg.Done()
				tr := rt.executeSingleTool(ctx, sessionID, inv, emit, toolExec, rt.asyncTimeout)
				mu.Lock()
				results[i] = tr
				mu.Unlock()
			}()
		} else {
			tr := rt.executeSingleTool(ctx, sessionID, inv, emit, toolExec, rt.syncTimeout)
			results[i] = tr
		}
	}

	wg.Wait()
	return results
}

// isToolAsync 判断工具是否应异步并发执行。
func (rt *Runtime) isToolAsync(name string) bool {
	if t, ok := rt.toolReg.Get(name); ok {
		return t.Info().IsAsync
	}
	return false
}

// executeSingleTool 执行单个工具调用，包含完整的 ToolHook 链支持。
//
// 执行流程：
//  1. ToolHook.Before 链（任一 hook 可中止）
//  2. 发送 ToolExecStart 事件
//  3. 创建带超时的 context
//  4. 调用 ToolExecutor.Execute
//  5. 根据执行结果或错误构造 ToolResult
//  6. ToolHook.After 链（任一 hook 可修改/中止结果）
//  7. 发送 ToolExecEnd 事件
//  8. 返回 ToolResult
func (rt *Runtime) executeSingleTool(
	ctx context.Context,
	sessionID string,
	inv hooks.ToolCallInvocation,
	emit func(events.ReactEventType, any),
	toolExec tools.ToolExecutor,
	timeout time.Duration,
) hooks.ToolResult {
	start := time.Now()

	// ToolHook.Before
	for _, h := range rt.toolHooks {
		hr := h.Before(sessionID, inv.Name, inv.Arguments)
		if hr.SkipWithResult != nil {
			emit(events.ToolExecStart, events.ToolExecStartData{
				ToolName: inv.Name,
				Params:   inv.Arguments,
			})
			result := *hr.SkipWithResult
			result.ToolCallID = inv.ID
			emit(events.ToolExecEnd, events.ToolExecEndData{
				ToolName:   result.ToolName,
				ToolCallID: result.ToolCallID,
				Duration:   result.Duration,
				Success:    result.Success,
				Result:     result.Result,
			})
			return result
		}
		if hr.Abort {
			emit(events.PermissionDenied, hr.AbortReason)
			return failedToolResult(inv.Name, inv.ID, "aborted: "+hr.AbortReason, start)
		}
		if hr.Error != nil {
			emit(events.PermissionDenied, hr.Error.Error())
			return failedToolResult(inv.Name, inv.ID, hr.Error.Error(), start)
		}
	}

	emit(events.ToolExecStart, events.ToolExecStartData{
		ToolName: inv.Name,
		Params:   inv.Arguments,
	})
	execCtx, execCancel := context.WithTimeout(ctx, timeout)
	defer execCancel()

	execResult, execErr := toolExec.Execute(execCtx, inv.Name, inv.Arguments)
	tr := buildToolResult(inv, execResult, execErr, start)

	// ToolHook.After
	for _, h := range rt.toolHooks {
		hr := h.After(&tr)
		if hr.Abort {
			tr = failedToolResult(inv.Name, inv.ID, "aborted: "+hr.AbortReason, start)
			break
		}
		if hr.Error != nil {
			tr = failedToolResult(inv.Name, inv.ID, hr.Error.Error(), start)
			break
		}
	}

	emit(events.ToolExecEnd, events.ToolExecEndData{
		ToolName:   tr.ToolName,
		ToolCallID: tr.ToolCallID,
		Duration:   time.Since(start),
		Success:    tr.Success,
		Result:     tr.Result,
		Error:      tr.Error,
	})
	return tr
}
