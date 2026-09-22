package agents

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/DotNetAge/goagent"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
)

// runWithGoAgent 是 AskBuilder.Run 的实现：以 goagent 引擎驱动多轮
// 思考-工具循环。
//
// 职责分工：
//   - 本函数：会话上下文准备（压缩、魔法词解析、用户消息持久化）、goagent
//     Agent 装配（History/System/Tools/LLM/事件桥接/RuntimeValue）、消息回写、
//     终止原因映射与 RunResult 组装；
//   - goagent 引擎：Think Loop 循环骨架、LLM 交互、工具调度、挂起检测、
//     控制平面登记（DefaultRuntimeManager）。
//
// 事件面：所有 ReactEvent 经 prepareEventBus + eventBridge 发射，
// mindx 的 AskBuilder.OnXxx 消费方式零变化。
func (rt *Runtime) runWithGoAgent(b *AskBuilder) (*RunResult, error) {
	ctx := b.ctx
	sid := b.session.ID()
	start := time.Now()

	rt.logger.Info("runWithGoAgent started", "session", sid, "agent", b.agentName, "model", rt.model.Name)

	// ── 1. 事件装配（复用旧装配：emit/emitRaw、OnEvent 分发、压缩事件、父级转发）──
	emit, emitRaw, outCtx, cleanup := prepareEventBus(b, rt.logger, ctx)
	defer cleanup()
	// 注入父级发射器的新 ctx 回写 builder：子代理 spawn 经 ctx 获取 parentEmit（旧核行为）
	b.ctx = outCtx

	defer func() {
		emit(events.ExecutionSummary, events.ExecutionSummaryData{
			TotalIterations:   b.resultIterations,
			TotalDuration:     b.resultDuration,
			TokensUsed:        b.resultUsage,
			TerminationReason: b.resultTerminationReason,
		})
		rt.logger.Info("runWithGoAgent finished",
			"session", sid,
			"agent", b.agentName,
			"reason", b.resultTerminationReason,
			"iterations", b.resultIterations,
			"duration_ms", time.Since(start).Milliseconds(),
			"answer_len", len(b.resultAnswer),
			"has_error", b.resultErr != nil,
		)
	}()

	// ── 2. 轮次开始前的会话准备 ──
	// 预加载会话元数据 + 偏移量模型压缩检查（与旧核 exec 相同的时机语义）
	b.session.Current()
	b.session.TryCompact(ctx)

	// 工具执行器：魔法词解析复用旧实现（内部执行挂起工具需要 goharness 执行器）
	toolExec := tools.NewToolExecutor(rt.toolReg,
		tools.WithEventEmitter(emitRaw),
		tools.WithLogger(rt.logger),
		tools.WithSession(b.session),
		tools.WithSessionStore(rt.sessionStore),
		tools.WithKVStore(rt.kvStore),
	)

	// ── 3. 权限魔法词解析（挂起恢复路径：mindx 以魔法词重新 Ask 进入）──
	// 挂起授权已在上轮保存到 session.PendingPermission；Allow 执行挂起工具、
	// Deny 合成拒绝结果，工具结果消息追加后由下方 goagent 引擎接续思考。
	magicHandled, magicRouted := rt.resolvePermissionMagicWord(ctx, b, toolExec, emit, rt.logger)
	if magicHandled {
		b.question = ""
		if magicRouted {
			// 纯路由到子会话：主会话没有新信息需要消化，直接结束本轮
			b.resultTerminationReason = "magicword_consumed"
			b.resultIterations = 0
			b.resultDuration = time.Since(start)
			return b.runResult(), nil
		}
	} else {
		// 用户消息持久化 + UserMessageSaved 事件（产品级事件，前端实时回收本轮依赖）。
		// 主会话挂起提问恢复：上一轮以 ask_user_pending 终止时登记了挂起提问，
		// 用户的本条消息即回答（ask_user_pending 产品语义），以 AskUser 工具结果
		// 消息补全协议（assistant.tool_calls 与 tool 消息严格配对，提问-回答
		// 上下文对 LLM 完整可见）；缺失 ToolCallID 时（异常兜底）退回旧的
		// user 消息注入方式。
		ts := time.Now().Unix()
		msg := session.Message{Role: "user", Content: b.question, Images: b.images, Timestamp: ts}
		if pendingAsk := b.session.TakePendingAskUser(); pendingAsk != nil && pendingAsk.ToolCallID != "" {
			msg = session.Message{Role: "tool", ToolCallID: pendingAsk.ToolCallID, Content: b.question, Timestamp: ts}
		}
		if err := b.session.Append(ctx, msg); err != nil {
			rt.logger.Error("追加用户消息失败", err, "session", sid)
			emit(events.Error, fmt.Sprintf("追加用户消息失败: %v", err))
			b.resultErr = fmt.Errorf("追加用户消息失败: %w", err)
			b.resultTerminationReason = "error"
			return b.runResult(), b.resultErr
		}
		emit(events.UserMessageSaved, events.UserMessageSavedData{Timestamp: ts})
		if msg.Role == "tool" {
			// 回答已作为 AskUser 工具结果注入会话，question 置空避免步骤 4 重复注入
			b.question = ""
		}
	}

	// ── 4. 组装 goagent 引擎输入 ──
	// 预防性修复历史遗留的工具调用配对断裂（并发交错产生的坏序列会被
	// OpenAI 兼容接口以 400 拒答）：旧核在 LLM 调用失败后反应式修复重试，
	// goagent 内核不暴露单轮重试语义，改为每轮入口预防性修复。
	if repaired, removedTCs := repairToolPairingBreak(ctx, b.session, rt.logger); repaired {
		rt.logger.Info("本轮入口已修复工具调用配对断裂", "session", sid)
		// 坏轮次回滚可能连带作废挂起授权/挂起提问，做对应闭环处置
		rt.invalidatePendingsAfterRepair(b, removedTCs, emit)
	}
	// 窗口末尾已是本轮问题（上面已追加）时 question 传空，避免重复注入
	windowHasQuestion := false
	window := b.session.Current()
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
	// AssembleMessages 输出 = [system 段] + 窗口转换（复用旧装配：图片多模态、
	// 孤儿 tool_call 过滤、思考内容透传）。整段作为 History 注入 goagent
	//（goagent 的 System 留空，system 消息随 History 注入效果一致）。
	msgs := AssembleMessages(rt.prompt.BuildSystemPrompts(sid, b.session), window, question)
	baseLen := len(msgs) // 消息回写的基准：此后为引擎新增消息

	// 工具排除集合（Agent 声明 ExcludeTools + 子会话多 Agent 工具屏蔽）
	excludeSet := effectiveExcludeTools(rt, b.agentName, b.session)

	// 会话级工具上下文：经 RuntimeValue 注入，适配器执行时取回（15+ 工具零改动）
	toolCtx := &tools.ToolContext{
		EmitEvent:    emitRaw,
		SessionStore: rt.sessionStore,
		KVStore:      rt.kvStore,
		Logger:       rt.logger,
		Session:      b.session,
	}
	if wl := b.session.Whitelist(); wl != nil {
		toolCtx.SessionWhitelist = wl
	}

	// 钩子协调器：聚合工具结果累积 / 钩子中止信息 / 重复错误引导 / 图片视觉消息
	coord := newHookCoord()

	// 事件桥接器（单 Run 生命周期）
	conversationID := session.NewRecordID()
	bridge := newEventBridge(rt, b, emit, conversationID, start, coord)

	maxIter := rt.model.MaxTurns
	if maxIter <= 0 {
		maxIter = defaultMaxIterations
	}

	// ── 5. 装配并驱动 goagent 引擎 ──
	// 零值 Agent + Config 装配（Ask() 是带默认配置的捷径，宿主装配不适用）；
	// 每次运行自动进入 goagent 控制平面（DefaultRuntimeManager），可查询可取消。

	// ga 引擎实例：首次 assemble() 创建，挂起续跑时由 resumePrepare 重建
	//（Go 词法作用域要求先声明，供下方两个闭包捕获引用）。
	var ga *goagent.Agent

	// assemble 装配引擎实例（首次运行与挂起续跑共用，保证配置一致）：
	// 每次装配同步重建钩子协调器并回填桥接器（续跑后工具序号/累积计数重新起算）。
	// goagent 惯例：每轮运行新实例即新 runtimeID 语义，对齐控制平面文档。
	assemble := func() *goagent.Agent {
		coord = newHookCoord()
		bridge.coord = coord
		g := (&goagent.Agent{}).
			History(msgs).
			Config(
				goagent.WithLLMClient(newClientAdapter(rt.llmClient, rt.model, rt.logger, emit)),
				goagent.WithToolExecutor(newToolExecutorAdapter(
					rt.toolReg, toolExec,
					func() map[string]bool { return excludeSet },
					rt.toolHooks, coord, emit, sid,
				)),
				goagent.WithEventBus(bridge),
				goagent.WithMaxIterations(maxIter),
				goagent.WithRuntimeValue(toolContextValue, toolCtx),
			)
		if len(rt.loopHooks) > 0 {
			g.Config(goagent.WithLoopHooks(adaptLoopHooks(rt.loopHooks, sid, b.agentName, b.session.ProjectDir(), coord, bridge)...))
		}
		return g
	}

	// resumePrepare 挂起续跑准备：重置桥接器挂起标记与最终答案缓存，重新组装
	// 消息窗口（含授权决策 / 用户回答等新增会话内容）并更新回写基准，重建引擎。
	resumePrepare := func() {
		bridge.suspendKind = ""
		bridge.finalAnswer = ""
		window = b.session.Current()
		msgs = AssembleMessages(rt.prompt.BuildSystemPrompts(sid, b.session), window, "")
		baseLen = len(msgs)
		ga = assemble()
	}

	ga = assemble()

	// 流式运行：增量事件经桥接器直通转发（非流式模式无 EvContentDelta/EvThinkingDelta）。
	// 最终答案由桥接器在 EvFinalAnswer 处捕获（ChatStreamCtx 只返回 error）。
	//
	// 子会话挂起编排（对齐控制平面文档的宿主编排路径）：
	// goagent 内核遇到需授权工具或 AskUser 提问时以 StopSuspended 结束当前
	// ChatStreamCtx，但子会话的冒泡语义要求挂起等待主会话路由决策 / 用户作答，
	// 结果注入会话后用新 runtimeID 对同一子会话再跑——因此子会话
	//（permissionCh / askCh 非 nil）的挂起不直接返回，而是阻塞等待后续跑。
	var err error
	for {
		err = ga.ChatStreamCtx(ctx, goagent.Callbacks{})

		// 每轮回写：引擎内存对话的新增消息持久化到会话
		bridge.writeBackNewMessages(b.session, baseLen)

		// 子会话挂起编排：permissionCh 非 nil（子会话）且本轮以 permission 挂起结束
		if bridge.suspendKind == "permission" && b.permissionCh != nil {
			pending := b.session.TakePendingPermission()
			if pending == nil {
				// 桥接器保存的 pending 被并发取走（如测试直接操作），退回终止
				break
			}
			rt.waitForPermissionDecision(ctx, b, pending, toolExec, emit, rt.logger)
			// 挂起期间用户停止（ctx 取消）或等待超时：退出编排循环
			if ctx.Err() != nil || b.resultTerminationReason == "permission_timeout" {
				err = ctx.Err()
				break
			}
			// 决策已补 tool 消息（applyPermissionAction 内 Append），续跑
			resumePrepare()
			continue
		}
		// 子会话挂起编排：askCh 非 nil（子会话）且本轮以 ask_user 挂起结束。
		// 提问卡片通知与挂起等待统一由 waitForAskUserDecision 发射/处理
		//（桥接器仅登记提问数据，避免通知重复）：用户作答经 askCh 送达并
		// 注入会话后续跑消化；取消/超时/注入失败则退出编排终止本轮。
		if bridge.suspendKind == "ask_user" && b.askCh != nil {
			rt.waitForAskUserDecision(ctx, b, bridge.lastAskPending, emit)
			if ctx.Err() != nil || b.resultTerminationReason != "" {
				err = ctx.Err()
				break
			}
			// 回答已注入会话（waitForAskUserDecision 内以 AskUser 工具结果消息
			// 补全协议），续跑
			resumePrepare()
			continue
		}
		// 主会话挂起（permission/ask_user）或正常结束：直接终止
		break
	}

	answer := bridge.finalAnswer

	// ── 7. 终止原因映射 + 结果组装 ──
	b.resultIterations = bridge.iterations
	b.resultDuration = time.Since(start)
	b.resultUsage = bridge.totalUsage
	switch {
	case coord.hookError != nil:
		// 钩子错误：优先于通用 err 分类（goagent 以 StopError 结束，err 即钩子错误）
		b.resultErr = coord.hookError
		b.resultTerminationReason = "hook_error"
	case err != nil && bridge.stopped == goagent.StopMaxIterations:
		// 达到最大轮数：与旧核一致按正常边界处理（非错误，前端以信息提示展示）
		b.resultTerminationReason = "max_iterations"
		b.resultAnswer = answer
		b.resultErr = nil
	case err != nil && errors.Is(err, context.Canceled):
		// 用户停止：与旧核一致的取消收尾事件，保证前端停止按钮有收尾信号
		rt.logger.Info("循环被用户取消", "session", sid)
		emit(events.LLMCancelled, events.LLMCancelledData{
			SessionID: sid,
			Elapsed:   time.Since(start),
		})
		b.resultTerminationReason = "cancelled"
		b.resultErr = err
	case err != nil && errors.Is(err, context.DeadlineExceeded):
		// LLM 调用超时：与旧核一致发射 LLMTimeout 事件并区分终止原因
		rt.logger.Error("LLM调用超时", err, "session", sid)
		emit(events.LLMTimeout, events.LLMTimeoutData{
			SessionID: sid,
			Timeout:   llmCallTimeout(rt.model.RequestTimeout, ctx),
			Elapsed:   time.Since(start),
			Error:     err.Error(),
		})
		b.resultErr = fmt.Errorf("LLM调用超时: %w", err)
		b.resultTerminationReason = "llm_timeout"
	case err != nil:
		rt.logger.Error("引擎运行失败", err, "session", sid)
		emit(events.Error, fmt.Sprintf("LLM调用失败: %v", err))
		b.resultErr = err
		b.resultTerminationReason = "llm_error"
	case coord.hookAbortReason != "":
		// 钩子中止：BeforeLLM 中止的答案为中止原因；AfterLLM 中止的答案为
		// LLM 本轮内容（对齐旧核语义，并经 FinalAnswer/TaskSummary 事件下发）
		b.resultTerminationReason = "hook_abort"
		if coord.hookAbortAnswer != "" {
			b.resultAnswer = coord.hookAbortAnswer
			emit(events.FinalAnswer, coord.hookAbortAnswer)
			emit(events.TaskSummary, events.TaskSummaryData{
				Summary:    coord.hookAbortAnswer,
				TokenUsage: bridge.totalUsage,
			})
		} else {
			b.resultAnswer = coord.hookAbortReason
		}
	case bridge.suspendKind == "permission":
		// 挂起等待授权：工具未执行，PendingPermission 已由桥接器保存，
		// 下一轮魔法词（PermissionAllow/PermissionDeny）恢复。
		// 保护：子会话 permission_timeout 路径 suspendKind 未重置，
		// waitForPermissionDecision 已设的原因不被覆盖。
		if b.resultTerminationReason == "" {
			b.resultTerminationReason = "permission_pending"
		}
		b.resultAnswer = answer
	case bridge.suspendKind == "ask_user":
		// 挂起等待用户回答：下一轮普通用户消息恢复。
		// 保护：子会话 ask_timeout 路径 suspendKind 未重置，
		// waitForAskUserDecision 已设的原因（ask_timeout/cancelled）不被覆盖。
		if b.resultTerminationReason == "" {
			b.resultTerminationReason = "ask_user_pending"
		}
		b.resultAnswer = answer
	case bridge.stopped == goagent.StopMaxIterations:
		b.resultTerminationReason = "max_iterations"
		b.resultAnswer = answer
	default:
		b.resultTerminationReason = "completed"
		b.resultAnswer = answer
	}
	return b.runResult(), b.resultErr
}

// runResult 汇总当前结果字段为 RunResult（终止原因兜底补齐，旧 Run 语义不变）。
func (b *AskBuilder) runResult() *RunResult {
	reason := b.resultTerminationReason
	if reason == "" {
		reason = "completed"
	}
	return &RunResult{
		Answer:            b.resultAnswer,
		TokenUsage:        b.resultUsage,
		Duration:          b.resultDuration,
		Iterations:        b.resultIterations,
		TerminationReason: reason,
	}
}
