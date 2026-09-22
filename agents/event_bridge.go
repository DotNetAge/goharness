package agents

import (
	"encoding/json"
	"time"

	"github.com/DotNetAge/goagent"
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/session"
	"github.com/google/uuid"
)

// eventBridge 将 goagent 引擎事件同步直通转换为 goharness ReactEvent（桥接器
// 实现 goagent.EventBus：Emit 即转换即发射，不缓冲、不二次订阅，满足阶段一
// 「token 级增量直通转发」的延迟要求）。
//
// 事件映射（计划 §事件四分类处置表的引擎级 + 挂起级条目）：
//
//	EvThinkingDelta/EvContentDelta/EvToolUseDelta/EvThinkingDone → 同名 ReactEvent
//	EvToolExecStart/EvToolExecEnd                                → 同名 ReactEvent（回填轮次 usage）
//	EvFinalAnswer                                                → FinalAnswer + TaskSummary
//	EvLoopEnd                                                    → LoopEnd（Iteration 从 0 基转为 1 基）
//	EvTokenUsage                                                 → TokenUsageRecorded（持久化 + 累计记账）
//	EvSuspend(kind=permission)                                   → PendingPermission 保存 + PermissionPending
//	EvSuspend(kind=ask_user)                                     → AskUserPending
//	EvStop(StopMaxIterations)                                    → MaxTurnsReached
//	EvComplete                                                   → 捕获对话上下文（供消息回写会话）
//	EvStop                                                       → 记录终止原因（Run 收尾映射 TerminationReason）
//
// 并发模型：goagent execLoop 在单 goroutine 内同步 Emit，桥接器的记账状态
// 无需加锁；桥接器生命周期与单次 Run 一致，不可跨 Run 复用。
type eventBridge struct {
	emit func(events.ReactEventType, any)
	b    *AskBuilder
	rt   *Runtime
	// coord 为钩子协调器（可 nil）：图片视觉消息回写时按工具序号查配对。
	coord *hookCoord

	// 记账元信息（TokenUsageRecord 持久化所需）
	sessionID      string
	agentName      string
	conversationID string
	model          config.ModelConfig
	start          time.Time

	// 运行期状态（单 goroutine 访问）
	totalUsage   session.TokenUsage
	iterations   int
	lastUsage    *session.TokenUsage // 最近一轮 LLM 调用 usage（回填 ToolExecEnd）
	stopped      goagent.StopReason
	suspendKind  string // 最近一次挂起类别："permission" / "ask_user"
	finalAnswer  string // EvFinalAnswer 捕获的最终答案（ChatStreamCtx 只返回 error）
	completeMsgs []gochatcore.Message
	// lastAskPending 登记子会话 ask_user 挂起的提问数据：提问卡片通知与挂起
	// 等待统一由编排分支的 waitForAskUserDecision 负责，此处仅暂存避免重复发射
	lastAskPending events.AskUserPendingData
}

// newEventBridge 创建桥接器。b/rt 供挂起映射（PendingPermission 保存、子会话
// sink 直达）与 TokenUsageStore 持久化使用；coord 供图片视觉消息回写配对。
func newEventBridge(rt *Runtime, b *AskBuilder, emit func(events.ReactEventType, any), conversationID string, start time.Time, coord *hookCoord) *eventBridge {
	return &eventBridge{
		emit:           emit,
		b:              b,
		rt:             rt,
		coord:          coord,
		sessionID:      b.session.ID(),
		agentName:      b.agentName,
		conversationID: conversationID,
		model:          rt.model,
		start:          start,
	}
}

// Emit 实现 goagent.EventBus：逐事件转换为 ReactEvent 发射。
func (br *eventBridge) Emit(ev goagent.Event) {
	switch d := ev.Data.(type) {
	case nil: // EvThinkingDone：思考段显式边界（前端据此把思考节点置为完成态）
		if ev.Type == goagent.EvThinkingDone {
			br.emit(events.ThinkingDone, nil)
		}
	case string: // EvThinkingDelta / EvContentDelta / EvFinalAnswer
		switch ev.Type {
		case goagent.EvThinkingDelta:
			br.emit(events.ThinkingDelta, d)
		case goagent.EvContentDelta:
			br.emit(events.ContentDelta, d)
		case goagent.EvFinalAnswer:
			br.finalAnswer = d
			br.emit(events.FinalAnswer, d)
			br.emit(events.TaskSummary, events.TaskSummaryData{
				Summary:    d,
				TokenUsage: br.totalUsage,
			})
		}
	case gochatcore.ToolCallDelta: // EvToolUseDelta
		if ev.Type == goagent.EvToolUseDelta {
			br.emit(events.ToolUseDelta, events.ToolUseDeltaData{
				Index:     d.Index,
				ID:        d.ID,
				Name:      d.Name,
				Arguments: d.Arguments,
			})
		}
	case *goagent.ToolExecStartData:
		br.emit(events.ToolExecStart, events.ToolExecStartData{
			ToolName: d.Name,
			Params:   rawMessageToMap(d.Args),
		})
	case *goagent.ToolExecEndData:
		ed := events.ToolExecEndData{
			ToolName:   d.Name,
			ToolCallID: d.ToolCallID,
			Success:    d.Success,
			Result:     d.Result,
			Duration:   d.Duration,
		}
		if d.Error != nil {
			ed.Error = d.Error.Error()
		}
		// 回填该工具调用所在轮次 LLM 调用的真实 usage（goagent 事件顺序：
		// EvTokenUsage 先于 EvToolExecEnd，lastUsage 即当前轮 usage）
		if br.lastUsage != nil {
			ed.PromptTokens = br.lastUsage.PromptTokens
			ed.CompletionTokens = br.lastUsage.CompletionTokens
			ed.TotalTokens = br.lastUsage.TotalTokens
			ed.CachedTokens = br.lastUsage.CachedTokens
		}
		br.emit(events.ToolExecEnd, ed)
	case *goagent.LoopEndData:
		// 迭代计数取最大值：直接回答场景 goagent 不发 LoopEnd，
		// 计数由 EvTokenUsage（每轮 LLM 调用必发）承载
		br.iterations = max(br.iterations, d.Iteration+1)
		br.emit(events.LoopEnd, events.CycleInfo{
			Iteration: br.iterations,
			Duration:  time.Since(br.start),
		})
	case *goagent.TokenUsageEvent:
		br.recordUsage(d)
	case *goagent.ExternalInputRequest:
		br.handleSuspend(d)
	case goagent.StopReason:
		br.stopped = d
		if d == goagent.StopFinished {
			// 正常收尾补发 loop_end（旧核契约：termination_reason=completed
			// 表示本轮已产出最终答案，前端据此置会话级收尾标记）。goagent
			// 收尾轮不发 EvLoopEnd，此处以 StopFinished 兜底对齐旧契约。
			br.emit(events.LoopEnd, events.CycleInfo{
				Iteration:         br.iterations,
				Duration:          time.Since(br.start),
				TerminationReason: "completed",
			})
		}
		if d == goagent.StopMaxIterations {
			br.emit(events.MaxTurnsReached, events.MaxTurnsReachedData{
				TurnsCompleted: br.iterations,
				MaxTurns:       br.maxIterations(),
				Suggestion:     "已达到最大思考轮次。任务可能需要更详细的指令或分步完成。你可以发送\"继续\"让 AI 基于当前进度继续，或者提供更具体的指导。",
			})
		}
	case []gochatcore.Message: // EvComplete：捕获对话上下文供消息回写（不外发）
		if ev.Type == goagent.EvComplete {
			br.completeMsgs = d
		}
	}
}

// Subscribe 实现 goagent.EventBus：桥接器是直通转换器而非广播总线，
// 订阅语义无意义，返回已关闭通道兜底。
func (br *eventBridge) Subscribe() (<-chan goagent.Event, func()) {
	ch := make(chan goagent.Event)
	close(ch)
	return ch, func() {}
}

// Close 实现 goagent.EventBus：桥接器无资源需要释放。
func (br *eventBridge) Close() {}

// recordUsage 处理 EvTokenUsage：迭代计数 + 累计记账 + 持久化 + TokenUsageRecorded 事件。
func (br *eventBridge) recordUsage(ev *goagent.TokenUsageEvent) {
	// 迭代计数不依赖 usage：每轮 LLM 调用必发 TokenUsage（0 基轮次），
	// 直接回答场景 goagent 不发 LoopEnd，计数以此为准（对齐旧核 lastIteration = iter+1）。
	// provider 未返回 usage 时仅跳过记账与持久化（旧核的内容长度估算逻辑属旧核专属，
	// 阶段一降级，后续如需要可将估算下沉到 clientAdapter 层）。
	br.iterations = max(br.iterations, ev.Iteration+1)
	if ev.Usage == nil {
		return
	}
	callUsage := session.TokenUsage{
		Timestamp:        time.Now(),
		PromptTokens:     ev.Usage.PromptTokens,
		CompletionTokens: ev.Usage.CompletionTokens,
		TotalTokens:      ev.Usage.TotalTokens,
	}
	if ev.Usage.PromptTokensDetails != nil {
		callUsage.CachedTokens = ev.Usage.PromptTokensDetails.CachedTokens
	}
	if ev.Usage.CompletionTokensDetails != nil {
		callUsage.ReasoningTokens = ev.Usage.CompletionTokensDetails.ReasoningTokens
	}
	br.lastUsage = &callUsage

	record := session.TokenUsageRecord{
		ID:               session.NewRecordID(),
		SessionID:        br.sessionID,
		ConversationID:   br.conversationID,
		ModelName:        br.model.Name,
		ProviderName:     br.model.Provider,
		AgentName:        br.agentName,
		PromptTokens:     callUsage.PromptTokens,
		CompletionTokens: callUsage.CompletionTokens,
		CachedTokens:     callUsage.CachedTokens,
		ReasoningTokens:  callUsage.ReasoningTokens,
		TotalTokens:      callUsage.TotalTokens,
		Timestamp:        time.Now(),
	}
	if err := br.rt.tokenUsageStore.Append(br.b.ctx, record); err != nil {
		br.rt.logger.Error("添加词元使用记录失败", err, "session", br.sessionID)
	} else {
		br.emit(events.TokenUsageRecorded, record)
	}
	br.totalUsage.PromptTokens += callUsage.PromptTokens
	br.totalUsage.CompletionTokens += callUsage.CompletionTokens
	br.totalUsage.TotalTokens += callUsage.TotalTokens
	br.totalUsage.CachedTokens += callUsage.CachedTokens
	br.totalUsage.ReasoningTokens += callUsage.ReasoningTokens
	br.totalUsage.Timestamp = time.Now()
}

// handleSuspend 处理 EvSuspend：按挂起类别映射为 goharness 挂起级事件，
// 并保存 PendingPermission 供魔法词解析（permission 类）。
func (br *eventBridge) handleSuspend(req *goagent.ExternalInputRequest) {
	switch req.Kind {
	case "permission":
		br.suspendKind = "permission"
		details, _ := req.Details.(*PermissionRequestDetails)
		reason := ""
		if details != nil {
			reason = details.Reason
		}
		// 过期授权闭环：新授权请求覆盖旧挂起时（ToolCallID 不同），取走旧
		// pending 并发射 PermissionDenied 让前端撤下过期弹窗，避免后续
		// 魔法词/决策命中已被取代的旧请求。
		if old := br.b.session.PendingPermission(); old != nil && old.ToolCallID != req.ToolCallID {
			br.b.session.TakePendingPermission()
			br.emit(events.PermissionDenied, "授权已过期：会话已产生新的待授权操作，原请求已被取代")
		}
		// 保存待处理授权（与旧核 checkPermissionGrants 相同的会话状态），
		// 供下一轮魔法词（PermissionAllow/PermissionDeny）解析
		securityLevel := events.LevelSafe
		if tool, ok := br.rt.toolReg.Get(req.ToolName); ok {
			securityLevel = tool.Info().SecurityLevel
		}
		br.b.session.SetPendingPermission(session.PendingPermission{
			ToolName:      req.ToolName,
			ToolCallID:    req.ToolCallID,
			Arguments:     rawMessageToMap(req.Arguments),
			Reason:        reason,
			SecurityLevel: securityLevelString(securityLevel),
		})
		data := events.PermissionPendingData{
			TickID:        uuid.New().String(),
			ToolName:      req.ToolName,
			Params:        rawMessageToMap(req.Arguments),
			Reason:        reason,
			SecurityLevel: securityLevel,
		}
		// 子会话授权冒泡：经旁路发送器直达前端（不依赖父 exec EventBus 存活）
		if br.b.permissionCh != nil {
			data.SessionID = br.sessionID
			if sink := br.b.permissionSink; sink != nil {
				sink(data)
				return
			}
		}
		br.emit(events.PermissionPending, data)
		br.emit(events.TaskSummary, events.TaskSummaryData{
			Summary:    "请求授权执行工具: " + req.ToolName + "，等待用户批准...",
			TokenUsage: br.totalUsage,
		})
	case "ask_user":
		br.suspendKind = "ask_user"
		details, _ := req.Details.(*AskUserRequestDetails)
		if details == nil {
			details = &AskUserRequestDetails{}
		}
		data := events.AskUserPendingData{
			Questions: []events.AskUserQuestion{{
				Question:    details.Question,
				Options:     details.Options,
				MultiSelect: details.MultiSelect,
			}},
			// ToolCallID 供恢复路径以 AskUser 工具结果消息补全协议
			ToolCallID: req.ToolCallID,
		}
		if br.b.askCh != nil {
			// 子会话：仅登记提问数据。提问卡片通知与挂起等待统一由编排分支的
			// waitForAskUserDecision 负责（镜像旧核分工：emit 提问 + 等待回答一体），
			// 此处提前发射会导致提问通知重复。
			data.SessionID = br.sessionID
			br.lastAskPending = data
			return
		}
		// 主会话：编排分支不接管（挂起即终止，等用户普通消息恢复）。
		// 登记挂起提问（跨轮持久化到会话目录），下一轮入口把用户消息视为
		// 回答并以 AskUser 工具结果消息补全协议；提问通知在桥接器直接发射。
		br.b.session.SetPendingAskUser(session.PendingAskUser{
			ToolCallID: req.ToolCallID,
			Question:   details.Question,
		})
		br.emit(events.AskUserPending, data)
		br.emit(events.TaskSummary, events.TaskSummaryData{
			Summary:    "向用户提出问题，等待回答中...",
			TokenUsage: br.totalUsage,
		})
	}
}

// maxIterations 返回本次运行的最大轮次（MaxTurnsReached 事件展示用）。
func (br *eventBridge) maxIterations() int {
	if br.rt.model.MaxTurns > 0 {
		return br.rt.model.MaxTurns
	}
	return defaultMaxIterations
}

// writeBackNewMessages 将 goagent 内存对话中的新增消息回写会话。
// msgs 是 goagent EvComplete 携带的完整对话上下文；baseLen 是 Run 装配时
// 注入 History 的消息数（system 段 + 会话窗口转换），其后即本轮新增消息
// （assistant / tool / 兜底注入的 user 消息）。
//
// 降级说明（阶段一）：assistant 消息的 FinishReason 与 Usage 不经 goagent
// 内存消息传递，回写时留空；FinishReason 的「答案边界」信号由桥接器在
// 正常收尾时为最后一条 assistant 消息补 "stop"。
func (br *eventBridge) writeBackNewMessages(sess *session.Session, baseLen int) {
	msgs := br.completeMsgs
	if len(msgs) <= baseLen {
		return
	}
	// 待配对队列：assistant 的 tool_calls 按序入队（空 ID 先分配合成 ID），
	// 后续 tool 消息缺失 tool_call_id 时弹出队首回填，保证 assistant/tool 严格配对
	// （与旧核 buildAssistantMessage 口径一致）。
	var pendingCalls []string
	// 工具结果序号：回写范围内每条 tool 消息递增，与 hookCoord 的工具执行
	// 计数对齐，用于图片视觉消息的顺序配对（对齐旧核 persistToolResults）。
	toolSeq := 0
	// pendingImages 为当前 tool 消息对应的待插图片块（tool 消息落库后追加）。
	var pendingImages imageEntry
	for i := baseLen; i < len(msgs); i++ {
		m := msgs[i]
		sm := session.Message{Timestamp: time.Now().Unix()}
		switch m.Role {
		case gochatcore.RoleAssistant:
			sm.Role = "assistant"
			sm.Content = textOfBlocks(m.Content)
			sm.ReasoningContent = m.ReasoningContent
			for _, tc := range m.ToolCalls {
				id := tc.ID
				if id == "" {
					id = "syn_" + session.NewRecordID()
				}
				pendingCalls = append(pendingCalls, id)
				sm.ToolCalls = append(sm.ToolCalls, session.ToolCall{
					ID: id, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
			// 最后一条 assistant 且本轮正常收尾（无工具调用）：补 finishReason
			if i == len(msgs)-1 && len(m.ToolCalls) == 0 && br.stopped == goagent.StopFinished {
				sm.FinishReason = "stop"
			}
		case gochatcore.RoleTool:
			sm.Role = "tool"
			sm.Content = textOfBlocks(m.Content)
			if m.ToolCallID != "" {
				sm.ToolCallID = m.ToolCallID
			} else if len(pendingCalls) > 0 {
				sm.ToolCallID = pendingCalls[0]
				pendingCalls = pendingCalls[1:]
			}
			// 图片视觉消息待插块：ImageHook 转换的图片块以 user 角色消息追加，
			// 紧随对应工具结果之后（对齐旧核 persistToolResults 的注入位置）。
			// 此处仅取出，待 tool 消息落库后再 Append，保证消息顺序。
			if br.coord != nil {
				for _, entry := range br.coord.imageEntries {
					if entry.toolSeq == toolSeq && len(entry.blocks) > 0 {
						pendingImages = entry
						break
					}
				}
			}
			toolSeq++
		default:
			sm.Role = string(m.Role)
			sm.Content = textOfBlocks(m.Content)
		}
		if err := sess.Append(br.b.ctx, sm); err != nil {
			br.rt.logger.Error("回写消息失败", err, "session", br.sessionID, "role", sm.Role)
			return
		}
		// tool 消息落库后追加图片视觉消息（紧随对应工具结果之后）
		if sm.Role == "tool" && len(pendingImages.blocks) > 0 {
			if err := sess.Append(br.b.ctx, session.Message{
				Role: "user",
				Content: "以下是工具 " + pendingImages.toolName +
					" 读取到的图片内容（视觉消息），请结合图片进行分析：",
				Images:    pendingImages.blocks,
				Timestamp: time.Now().Unix(),
			}); err != nil {
				br.rt.logger.Error("追加图片视觉消息失败", err, "session", br.sessionID)
				return
			}
			pendingImages = imageEntry{}
		}
	}
}

// textOfBlocks 拼接消息的文本内容块（goagent 内存消息 → session 消息的文本提取）。
func textOfBlocks(blocks []gochatcore.ContentBlock) string {
	out := ""
	for _, b := range blocks {
		if b.Type == gochatcore.ContentTypeText {
			if out != "" {
				out += "\n"
			}
			out += b.Text
		}
	}
	return out
}

// rawMessageToMap 将 json.RawMessage 解析为参数 map（工具事件 UI 展示用）；
// 解析失败返回 nil（不阻断事件流）。
func rawMessageToMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	m := map[string]any{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
