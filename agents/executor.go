package agents

import (
	"context"
	"errors"
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

// llmCallTimeout 决定单次 LLM 调用的超时预算，优先级从高到低：
//  1. 模型配置的 RequestTimeout（秒）：按模型显式配置，适配慢模型（思考/首 token 耗时远超默认值）；
//  2. 否则跟随 ctx 截止时间的剩余时长（调用方设置的整体预算）；
//  3. 否则回退默认 defaultLLMTimeout（4 分钟）。
func llmCallTimeout(requestTimeoutSec int64, ctx context.Context) time.Duration {
	if requestTimeoutSec > 0 {
		return time.Duration(requestTimeoutSec) * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining
		}
	}
	return defaultLLMTimeout
}

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

	// 事件总线：创建 EventBus、注册事件分发回调与压缩事件处理器，返回 emit/emitRaw 闭包与 cleanup。
	emit, emitRaw, ctx, cancelEventBus := prepareEventBus(b, logger, ctx)
	defer cancelEventBus()

	conversationID := session.NewRecordID()
	var totalUsage = session.TokenUsage{Timestamp: time.Now()}
	defer func() {
		emit(events.ExecutionSummary, events.ExecutionSummaryData{
			TotalIterations:   b.resultIterations,
			TotalDuration:     b.resultDuration,
			TokensUsed:        totalUsage,
			TerminationReason: b.resultTerminationReason,
		})
	}()

	// 在创建工具执行器前预加载会话元数据，
	// 以便 WithProjectDirExecutor 从持久化的会话元数据中获取正确的项目目录。
	b.session.Current()

	// 偏移量模型（memmache.md）：在新一个轮次开始前检查活跃窗口 Token 是否超限，
	// 若超限则对 messages[cursor:] 全量摘要并清空（cursor = len(messages)）。
	// 不在 Append 末尾或 exec 循环中调用，避免工具结果 append 中途触发清空
	// 破坏 tool_call 配对。
	// 触发条件：windowTokens > 80% * ModelContextLength。对所有 ContextLength > 0
	// 的模型生效（maxWindowSize = ModelContextLength()，由 modelContextResolver 动态返回）。
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
		tools.WithEventEmitter(emitRaw),
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
	magicHandled, magicRouted := rt.resolvePermissionMagicWord(ctx, b, toolExec, emit, logger)

	// 魔法词已被消费时清空 b.question（避免下方循环将其作为普通用户消息重新注入 LLM 调用）；
	// 否则将用户消息追加到会话。用同一个 ts 构造消息和事件，避免两次 time.Now() 跨秒不一致。
	if magicHandled {
		b.question = ""
		// 纯路由到子会话：带目标魔法词只把授权决策转发给子会话（子会话已异步继续执行），
		// 主会话没有执行任何工具、也没有新信息需要消化。若继续进入 LLM 循环，
		// 会白跑一次调用且上下文与上一轮完全相同。直接结束本轮 exec。
		if magicRouted {
			b.resultTerminationReason = "magicword_consumed"
			return
		}
	} else {
		ts := time.Now().Unix()
		if !appendAndAbort(-1, session.Message{
			Role: "user", Content: b.question, Images: b.images, Timestamp: ts,
		}, "用户消息") {
			return
		}
		// 通知前端：user 消息已持久化，回传 Timestamp 用于实时回收本轮。
		emit(events.UserMessageSaved, events.UserMessageSavedData{Timestamp: ts})
	}

	// 使用全量工具定义：所有已注册工具一次性发送给 LLM，
	// 不在迭代间改变工具集，以保持前缀缓存稳定。
	// 应用当前 Agent 的 ExcludeTools 过滤，排除声明中不允许使用的工具。
	excludeTools := rt.prompt.AgentExcludeTools(b.agentName)
	allToolDefs := buildAllToolDefinitions(rt.toolReg, excludeTools)

	// 构建系统提示词段落（每轮之间静态不变）
	systemSections := rt.prompt.BuildSystemPrompts(sid, b.session)

	var lastIteration int
	var prevToolResults []hooks.ToolResult

	// ── 重复错误引导状态 ──
	// 本地小模型往往无法从工具错误反馈中自我纠正，容易陷入「反复用相同方式调用
	// 同一工具并得到相同错误」的死循环。dupErrorTracker 按工具名跟踪连续相同错误，
	// 达阈值时注入一次引导话术；详见 maybeGuide。
	dupTracker := newDupErrorTracker()

	for iter := 0; iter < maxIter; iter++ {
		if err := ctx.Err(); err != nil {
			b.resultErr = err
			b.resultTerminationReason = "cancelled"
			// 与流式调用中取消保持一致的取消事件，保证前端停止按钮有收尾事件
			emit(events.LLMCancelled, events.LLMCancelledData{
				SessionID: sid,
				Elapsed:   time.Since(start),
			})
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

		// 使用全量工具定义（所有工具一次性注册，保持前缀缓存稳定）
		toolDefs := allToolDefs

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

		// MicroCompact（方案 A）：仅对 128K < ContextLength <= 250K 的模型启用。
		// - ≤128K：由 TryCompact 独占管理（80% 触发全量摘要清空），不调用 MicroCompact
		// - 128K–250K：MicroCompact 在 45% 触发局部压缩，仅压缩 [25%, 65%] 位置范围内的
		//   工具消息，保留最近的 tool_call 配对；若窗口继续涨到 80%，TryCompact 全量清空
		// - >250K：不启用，避免修改上下文中间 tool 消息导致 KV 缓存重算成本过高
		if shouldEnableMicroCompact(b.session.ModelContextLength()) {
			b.session.TryMicroCompact()
		}

		// 重新读取窗口 —— Current() 返回 messages[cursor:] 的新副本。
		window = b.session.Current()

		msgs := AssembleMessages(callInput.SystemPromptSections, window, question)

		// ── 调试：打印完整系统提示词 ──
		// 调试时开启，生产时关闭，不要删除此代码。
		// logSystemPromptDebug(rt.logger, sid, iter, msgs)

		// ── 流式调用 LLM（失败自愈：工具配对错误时截断会话重试一次）──
		// 正常路径只尝试一次；仅当首次调用返回「工具调用配对不完整」类 400 错误时，
		// 自动截断会话中的坏轮次并重试一次（最多 2 次尝试），避免整个会话作废。
		// 超时按模型配置的 request_timeout 计算（未配置时跟随 ctx / 回退默认值）。
		// 使用单次调用独立截止时间：父 ctx 承载整个会话执行（多次迭代），
		// 不能把整体预算当作单次调用超时；同时超时到期后 collectStreamResponse 可立即感知并终止。
		llmTimeout := llmCallTimeout(rt.model.RequestTimeout, ctx)
		callCtx, callCancel := context.WithTimeout(ctx, llmTimeout)
		defer callCancel()
		var (
			stream *gochatcore.Stream
			err    error
		)
		for attempt := 1; ; attempt++ {
			logger.Info("LLM调用开始", "session", sid, "iter", iter, "attempt", attempt)
			stream, err = rt.llmClient.Stream(callCtx, LLMRequest{
				Messages:          msgs,
				Model:             rt.model.Name,
				Temperature:       rt.model.Temperature,
				TopP:              rt.model.TopP,
				TopK:              rt.model.TopK,
				RepetitionPenalty: rt.model.RepetitionPenalty,
				FrequencyPenalty:  rt.model.FrequencyPenalty,
				Tools:             toolDefs,
				ToolChoice:        "auto",
				Timeout:           llmTimeout,
				// 建流重试必须冒泡：限流/5xx 等可预知错误若静默重试，
				// 前端会一直显示「正在处理」，用户无从得知还要等多久。
				OnRetry: func(n LLMRetryNotice) {
					if n.Phase == LLMRetryPhaseRecovered {
						logger.Info("LLM建流重试成功", "session", sid, "iter", iter, "retries", n.Attempt)
						emit(events.LLMRetry, events.LLMRetryData{
							SessionID:   sid,
							Provider:    n.Provider,
							Model:       n.Model,
							Attempt:     n.Attempt,
							MaxAttempts: n.MaxAttempts,
							Phase:       events.LLMRetryPhaseRecovered,
						})
						return
					}
					logger.Warn("LLM建流失败，进入退避重试",
						"session", sid, "iter", iter,
						"attempt", n.Attempt, "max_attempts", n.MaxAttempts,
						"status_code", n.StatusCode, "retry_after", n.Delay.String(), "error", n.Err)
					emit(events.LLMRetry, events.LLMRetryData{
						SessionID:   sid,
						Provider:    n.Provider,
						Model:       n.Model,
						StatusCode:  n.StatusCode,
						Attempt:     n.Attempt,
						MaxAttempts: n.MaxAttempts,
						RetryAfter:  n.Delay,
						Error:       n.Err.Error(),
						Phase:       events.LLMRetryPhaseRetry,
					})
				},
			})
			if err == nil {
				break
			}
			if attempt < 2 && isToolPairingError(err) {
				logger.Warn("检测到工具调用配对错误，尝试截断会话后重试",
					"session", sid, "iter", iter, "error", err)
				if repairToolPairingBreak(ctx, b.session, logger) {
					// 会话已截断，重新读取窗口并组装消息，进入下一次尝试。
					window = b.session.Current()
					msgs = AssembleMessages(callInput.SystemPromptSections, window, question)
					continue
				}
				logger.Warn("会话修复失败或窗口内无配对断裂，不再重试",
					"session", sid, "iter", iter)
			}
			break
		}
		if err != nil {
			if errors.Is(err, context.Canceled) {
				logger.Info("LLM思想流被用户取消", "session", sid, "iter", iter, "elapsed", time.Since(start))
				emit(events.LLMCancelled, events.LLMCancelledData{
					SessionID: sid,
					Elapsed:   time.Since(start),
				})
				b.resultTerminationReason = "cancelled"
			} else if errors.Is(err, context.DeadlineExceeded) {
				logger.Error("LLM调用超时", err, "session", sid, "iter", iter)
				emit(events.LLMTimeout, events.LLMTimeoutData{
					SessionID: sid,
					Timeout:   llmTimeout,
					Elapsed:   time.Since(start),
					Error:     err.Error(),
				})
				b.resultErr = fmt.Errorf("LLM调用超时: %w", err)
				b.resultTerminationReason = "llm_timeout"
			} else {
				// 401 认证失败时输出实际请求的 url 与 Authorization，便于排查
				// 「配置了 key 却报 API Key not exists」类问题（常见根因：
				// 配置里写的是环境变量名，而 daemon 进程读不到该变量）。
				// 调试信息含 Key 明文，仅写入本地日志，不进入用户可见的事件流。
				if IsUnauthorizedLLMError(err) {
					logger.Error("LLM认证失败(401)调试信息", err, "session", sid, "iter", iter, "debug", llmAuthDebugInfo(rt.llmClient))
				}
				// 402 欠费：可预知的终止性错误，必须给出明确的人话提示而非笼统的「LLM调用失败」。
				if IsPaymentRequiredLLMError(err) {
					logger.Error("LLM服务商返回402（账户欠费）", err, "session", sid, "iter", iter, "provider", rt.model.Provider)
					emit(events.Error, fmt.Sprintf("服务商「%s」返回 402（账户欠费或余额不足），请充值或更换服务商后重试", rt.model.Provider))
					b.resultErr = err
					b.resultTerminationReason = "llm_error"
					setIterResult(iter)
					return
				}
				logger.Error("LLM调用失败", err, "session", sid, "iter", iter)
				emit(events.Error, fmt.Sprintf("LLM调用失败: %v", err))
				b.resultErr = err
				b.resultTerminationReason = "llm_error"
			}
			setIterResult(iter)
			return
		}

		// 流式读取并收集响应（content/reasoning/finishReason/错误）。
		// 工具调用由下方在确认无错误后通过 stream.ToolCalls() 获取，
		// 保持「错误时不收集工具调用」的原有行为。
		// 使用 callCtx：单次调用超时到期后立即终止读取，而非继续阻塞。
		content, reasoning, finishReason, streamErr := collectStreamResponse(callCtx, stream, emit)

		if streamErr != nil {
			if errors.Is(streamErr, context.Canceled) {
				logger.Info("LLM调用被用户取消", "session", sid, "iter", iter, "elapsed", time.Since(start))
				emit(events.LLMCancelled, events.LLMCancelledData{
					SessionID: sid,
					Elapsed:   time.Since(start),
				})
				b.resultTerminationReason = "cancelled"
			} else if errors.Is(streamErr, context.DeadlineExceeded) {
				logger.Error("LLM调用超时", streamErr, "session", sid, "iter", iter)
				emit(events.LLMTimeout, events.LLMTimeoutData{
					SessionID: sid,
					Timeout:   llmTimeout,
					Elapsed:   time.Since(start),
					Error:     streamErr.Error(),
				})
				b.resultErr = streamErr
				b.resultTerminationReason = "llm_timeout"
			} else {
				// 402 欠费：与建流失败分支同样的人话提示。
				if IsPaymentRequiredLLMError(streamErr) {
					logger.Error("LLM服务商返回402（账户欠费）", streamErr, "session", sid, "iter", iter, "provider", rt.model.Provider)
					emit(events.Error, fmt.Sprintf("服务商「%s」返回 402（账户欠费或余额不足），请充值或更换服务商后重试", rt.model.Provider))
					b.resultErr = streamErr
					b.resultTerminationReason = "llm_error"
					setIterResult(iter)
					return
				}
				logger.Error("LLM调用失败", streamErr, "session", sid, "iter", iter)
				emit(events.Error, fmt.Sprintf("LLM调用失败: %v", streamErr))
				b.resultErr = streamErr
				b.resultTerminationReason = "llm_error"
			}
			setIterResult(iter)
			return
		}

		// 收集流式调用中的工具调用
		streamToolCalls := stream.ToolCalls()

		emit(events.ThinkingDone, nil)

		// 记录 token 使用情况
		callUsage := rt.recordStreamUsage(ctx, sid, b.agentName, iter, stream, start, conversationID, &totalUsage, emit, logger)

		// 根据流式数据构造 LLM 响应，供 hooks 使用
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

		// 持久化助手消息（content + reasoning + tool_calls）；缺少 ID 的工具调用会被回填合成 ID，
		// 以满足 OpenAI 严格的 tool_call/tool 消息配对要求。详见 buildAssistantMessage。
		// finishReason 一并持久化：这是协议内建的「答案边界」信号（stop=最终回答 / tool_calls=继续循环），
		// 供前端恢复历史时精确区分过程 content 与最终答案。
		assistantMsg := buildAssistantMessage(content, reasoning, finishReason, callUsage, streamToolCalls, logger, sid, iter)
		if !appendAndAbort(iter, assistantMsg, "助手消息") {
			return
		}

		lastIteration = iter + 1
		emit(events.LoopEnd, events.CycleInfo{
			Iteration: lastIteration, Duration: time.Since(start),
		})

		// ── 没有工具调用 → 回答完成 ──
		if len(streamToolCalls) == 0 {
			rt.finalizeAnswer(b, content, reasoning, finishReason, totalUsage, lastIteration, start, emit)
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
			// 子智能体授权冒泡：子会话挂起等待主会话（用户）的授权决策，
			// 授权后执行挂起工具并继续循环，而不是像主会话那样以
			// permission_pending 终止并等待下一轮魔法词重新进入。
			// 主会话自身不设置 permissionCh，保持原有行为不变。
			if b.permissionCh != nil {
				logger.Info("子智能体循环挂起: 工具需要授权，等待主会话授权",
					"tool", pending.ToolName, "session", sid)
				rt.waitForPermissionDecision(ctx, b, pending, toolExec, emit, logger)
				if b.resultTerminationReason != "" {
					// 授权等待超时（permission_timeout）或上下文取消（cancelled）
					setIterResult(iter)
					return
				}
				// 授权决策已消费并执行挂起工具，继续下一轮循环
				continue
			}
			b.resultTerminationReason = "permission_pending"
			setIterResult(iter)
			logger.Info("循环暂停: 工具需要授权，等待用户批准", "tool", pending.ToolName)
			return
		}

		executed := make(map[string]struct{}, len(invocs))
		for _, inv := range invocs {
			executed[inv.ID] = struct{}{}
		}

		toolResults := rt.executeTools(ctx, sid, invocs, emit, toolExec, callUsage)
		prevToolResults = toolResults

		// 持久化工具结果（完整内容，不做截断）：含重复错误引导与图片视觉消息。
		// 追加失败时 persistToolResults 返回 false，调用方终止循环。
		if !rt.persistToolResults(iter, toolResults, dupTracker, sid, appendAndAbort) {
			return
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
			logger.Info("思考暂停: 已调用 AskUser 工具，等待用户回答")
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
	// 空结果属于"不及预期"场景，同样采用第一人称引导，提示调整参数或换工具。
	return fmt.Sprintf("[%s] 返回结果: (空结果)。我未能从该工具获得任何输出，下一步我应该考虑调整参数或改用其它工具来获取所需信息。", tr.ToolName)
}

// buildAllToolDefinitions 从工具注册表构建工具定义，用于 LLM 请求的 tools 字段。
// 所有工具一次性注册，不在迭代间改变工具集，以保持前缀缓存稳定。
// exclude 为非空时，被排除的工具不会出现在 tools 字段中，LLM 将无法调用它们。
func buildAllToolDefinitions(registry tools.ToolRegistry, exclude map[string]bool) []gochatcore.Tool {
	allTools := registry.All()
	if len(allTools) == 0 {
		return nil
	}
	out := make([]gochatcore.Tool, 0, len(allTools))
	for _, t := range allTools {
		info := t.Info()
		if exclude != nil && exclude[info.Name] {
			continue
		}
		out = append(out, gochatcore.Tool{
			Name:        info.Name,
			Description: info.Description,
			Parameters:  buildParamSchema(info.Parameters),
		})
	}
	return out
}

// collectStreamResponse 读取 LLM 流式响应，收集文本内容、推理内容与完成原因，
// 并将增量事件（content/thinking/toolCall）通过 emit 转发。流在读取完毕后关闭。
//
// 设计说明：工具调用（stream.ToolCalls()）刻意不在此函数内收集，而由调用方在
// 确认无错误后自行获取，以保持「流式出错时不收集工具调用」的原有行为。
func collectStreamResponse(ctx context.Context, stream *gochatcore.Stream, emit func(events.ReactEventType, any)) (content, reasoning, finishReason string, streamErr error) {
	var contentBuf, reasoningBuf strings.Builder
	for {
		// 优先响应 ctx 取消：HTTP 流被中断时流可能静默关闭（未携带 EventError），
		// 此处显式检查取消状态，确保停止按钮能及时终止本轮，而非以半截答案正常收尾。
		if err := ctx.Err(); err != nil {
			stream.Close()
			return contentBuf.String(), reasoningBuf.String(), "", err
		}
		if !stream.Next() {
			break
		}
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
	return contentBuf.String(), reasoningBuf.String(), finishReason, streamErr
}

// logSystemPromptDebug 在 Debug 级别打印完整系统提示词，便于排查提示词装配问题。
// logger 为 nil 时直接返回。仅收集 role=system 消息中的 text 内容块。
func logSystemPromptDebug(logger logging.Logger, sid string, iter int, msgs []gochatcore.Message) {
	if logger == nil {
		return
	}
	var sysTexts []string
	for _, m := range msgs {
		if m.Role != "system" {
			continue
		}
		for _, block := range m.Content {
			if block.Type == "text" {
				sysTexts = append(sysTexts, block.Text)
			}
		}
	}
	if len(sysTexts) > 0 {
		logger.Debug("===== SYSTEM PROMPT =====",
			"session_id", sid, "iter", iter, "system_prompt", strings.Join(sysTexts, "\n\n---\n\n"))
	}
}

// dupErrorTracker 跟踪同一工具连续出现的完全相同错误，达到阈值时生成引导话术，
// 提示大模型更换工作方式（而非硬性熔断循环）。
//
// 计数规则：
//   - 工具返回错误且错误文本未自带「下一步我应该」引导时，按工具名累计连续相同错误次数；
//     错误文本变化则重新从 1 开始计数。
//   - 工具成功执行、或错误已含引导时，重置该工具的全部计数状态，
//     确保后续若再出现错误从第 1 次开始计数。
//   - 引导话术对每个工具仅注入一次（guided 标记），避免每轮重复注入同一段话术。
//
// 注入的话术追加到 tool 消息末尾，不影响 system prompt 与 tools 字段构成的前缀缓存。
type dupErrorTracker struct {
	dupCount map[string]int
	lastErr  map[string]string
	guided   map[string]bool
}

func newDupErrorTracker() dupErrorTracker {
	return dupErrorTracker{
		dupCount: make(map[string]int),
		lastErr:  make(map[string]string),
		guided:   make(map[string]bool),
	}
}

// maybeGuide 根据工具结果决定是否生成重复错误引导话术。
// 返回 guide 为应追加到工具结果末尾的话术（空串表示不追加），count 为该工具当前的连续相同错误次数。
// map 为引用类型，故值接收器即可在调用间保持并修改状态。
func (t dupErrorTracker) maybeGuide(tr hooks.ToolResult) (guide string, count int) {
	// 工具成功执行或错误已含引导时，重置该工具的重复错误跟踪状态。
	if tr.Error == "" || strings.Contains(tr.Error, "下一步我应该") {
		delete(t.dupCount, tr.ToolName)
		delete(t.lastErr, tr.ToolName)
		delete(t.guided, tr.ToolName)
		return "", 0
	}
	if t.lastErr[tr.ToolName] == tr.Error {
		t.dupCount[tr.ToolName]++
	} else {
		t.lastErr[tr.ToolName] = tr.Error
		t.dupCount[tr.ToolName] = 1
	}
	count = t.dupCount[tr.ToolName]
	if count >= duplicateErrorThreshold && !t.guided[tr.ToolName] {
		t.guided[tr.ToolName] = true
		guide = fmt.Sprintf(duplicateErrorGuideTemplate, count, tr.ToolName)
	}
	return guide, count
}

// buildAssistantMessage 构造助手消息（content + reasoning + tool_calls），并为缺少
// tool_call_id 的工具调用回填合成 ID。
//
// 某些 LLM 提供商（DeepSeek、Qwen、本地 vLLM 等）可能在流式响应中 emit 工具调用但不带
// tool_call_id。OpenAI 严格消息格式要求每个 tool_call 后必须跟一条 tool_call_id 匹配的
// tool 消息，否则返回 400 "insufficient tool messages following tool_calls message"。
// 为保持 assistant.ToolCalls 与后续持久化的 tool.ToolCallID 同步，此处为缺失 ID 回填
// 合成 ID；该 ID 随后被 parseToolInvocations 和 executeTools 使用，故 pair 始终匹配。
//
// 注意：streamToolCalls 的 ID 回填会通过共享底层数组反映到调用方切片。
func buildAssistantMessage(content, reasoning, finishReason string, usage *session.TokenUsage, streamToolCalls []gochatcore.ToolCall, logger logging.Logger, sid string, iter int) session.Message {
	msg := session.Message{
		Role:             "assistant",
		Content:          content,
		ReasoningContent: reasoning,
		FinishReason:     finishReason,
		Timestamp:        time.Now().Unix(),
		Usage:            usage,
	}
	for i := range streamToolCalls {
		if streamToolCalls[i].ID == "" {
			streamToolCalls[i].ID = "syn_" + session.NewRecordID()
			logger.Warn("LLM调用了工具但没有包含ID，已填充合成的工具ID",
				"session", sid, "iter", iter, "tool", streamToolCalls[i].Name, "id", streamToolCalls[i].ID)
		}
		msg.ToolCalls = append(msg.ToolCalls, session.ToolCall{
			ID:        streamToolCalls[i].ID,
			Name:      streamToolCalls[i].Name,
			Arguments: streamToolCalls[i].Arguments,
		})
	}
	return msg
}

// persistToolResults 将工具执行结果逐一持久化为 tool 消息：格式化结果文本、按需注入
// 重复错误引导话术（dupTracker）、追加图片视觉消息。返回 ok=false 表示某次消息追加失败，
// 调用方应终止循环（appendMsg 已设置错误结果与终止原因）。
func (rt *Runtime) persistToolResults(
	iter int,
	toolResults []hooks.ToolResult,
	dupTracker dupErrorTracker,
	sid string,
	appendMsg func(int, session.Message, string) bool,
) bool {
	for _, tr := range toolResults {
		toolContent := formatToolResult(tr)

		// ── 重复错误引导 ──
		// 当同一工具连续出现 duplicateErrorThreshold 次完全相同的错误时，
		// 在工具结果末尾追加引导话术，提示大模型更换工作方式（而非硬性熔断循环）。
		// 引导话术注入的是动态追加的 tool 消息，不影响 system prompt 与
		// tools 字段构成的前缀缓存。
		// 注意：工具错误已含「下一步我应该」引导时（tools 层 Guide 体系），
		// 不再叠加反思话术，避免同一消息出现两个「下一步我应该」块造成冗余；
		// 仅对零包装的未知错误叠加反思引导，与 tools 层「兜底不给指引」互补。
		if guide, dupCnt := dupTracker.maybeGuide(tr); guide != "" {
			toolContent += "\n\n" + guide
			rt.logger.Warn("检测到工具重复相同错误，已注入引导话术",
				"session", sid, "tool", tr.ToolName, "iter", iter, "dup_count", dupCnt)
		}

		preview := toolContent
		if len(preview) > 120 {
			preview = preview[:120]
		}
		rt.logger.Info("工具结果已持久化", "session", sid, "tool", tr.ToolName, "content_len", len(toolContent), "content_preview", preview)
		if !appendMsg(iter, session.Message{
			Role: "tool", Content: toolContent, Timestamp: time.Now().Unix(),
			ToolCallID: tr.ToolCallID,
		}, "工具结果") {
			return false
		}

		// 图片消息：ImageHook 已把工具返回的图片转换为图片内容块。
		// 图片以 image_url 消息（user 角色）进入上下文，紧随对应的工具结果之后，
		// 而非混入工具结果的文本内容——tool 消息的 content 在 API 侧只能是字符串。
		if len(tr.ImageBlocks) > 0 {
			imgMsg := session.Message{
				Role: "user", Timestamp: time.Now().Unix(),
				Content: "以下是工具 " + tr.ToolName + " 读取到的图片内容（视觉消息），请结合图片进行分析：",
				Images:  tr.ImageBlocks,
			}
			rt.logger.Info("图片已作为视觉消息追加",
				"session", sid, "tool", tr.ToolName, "images", len(tr.ImageBlocks))
			if !appendMsg(iter, imgMsg, "图片消息") {
				return false
			}
		}
	}
	return true
}

// finalizeAnswer 在 LLM 未调用工具时收尾本轮 Ask：确定答案文本（content 为空时回退到
// reasoning）、按 finishReason 设置终止原因、发送 FinalAnswer/TaskSummary 事件并填写
// 结果字段。调用方应在调用后立即 return。
func (rt *Runtime) finalizeAnswer(
	b *AskBuilder,
	content, reasoning, finishReason string,
	totalUsage session.TokenUsage,
	lastIteration int,
	start time.Time,
	emit func(events.ReactEventType, any),
) {
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
	usage *session.TokenUsage,
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
				tr := rt.executeSingleTool(ctx, sessionID, inv, emit, toolExec, rt.asyncTimeout, usage)
				mu.Lock()
				results[i] = tr
				mu.Unlock()
			}()
		} else {
			tr := rt.executeSingleTool(ctx, sessionID, inv, emit, toolExec, rt.syncTimeout, usage)
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
	usage *session.TokenUsage,
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
			emit(events.ToolExecEnd, withToolUsage(events.ToolExecEndData{
				ToolName:   result.ToolName,
				ToolCallID: result.ToolCallID,
				Duration:   result.Duration,
				Success:    result.Success,
				Result:     result.Result,
			}, usage))
			return result
		}
		if hr.Abort {
			emit(events.PermissionDenied, hr.AbortReason)
			return failedToolResult(inv.Name, inv.ID, hr.AbortReason, start)
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
			tr = failedToolResult(inv.Name, inv.ID, hr.AbortReason, start)
			break
		}
		if hr.Error != nil {
			tr = failedToolResult(inv.Name, inv.ID, hr.Error.Error(), start)
			break
		}
	}

	emit(events.ToolExecEnd, withToolUsage(events.ToolExecEndData{
		ToolName:   tr.ToolName,
		ToolCallID: tr.ToolCallID,
		Duration:   time.Since(start),
		Success:    tr.Success,
		Result:     tr.Result,
		Error:      tr.Error,
	}, usage))
	return tr
}

// withToolUsage 将工具调用所在轮次 LLM 调用的实际 usage 回填到 ToolExecEnd 事件，
// 供前端「查看结果」展示真实 token 消耗。usage 为 nil（如授权后补执行的挂起工具，
// 无法归属到明确的 LLM 轮次）时保持零值不填充。
func withToolUsage(ed events.ToolExecEndData, usage *session.TokenUsage) events.ToolExecEndData {
	if usage == nil {
		return ed
	}
	ed.PromptTokens = usage.PromptTokens
	ed.CompletionTokens = usage.CompletionTokens
	ed.TotalTokens = usage.TotalTokens
	ed.CachedTokens = usage.CachedTokens
	return ed
}
