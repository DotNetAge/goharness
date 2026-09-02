package agents

import (
	"context"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/session"
)

type AskBuilder struct {
	ctx       context.Context
	cancel    context.CancelFunc
	agentName string
	question  string
	images    []session.ImageBlock // 随本次用户问题附加的图片内容块（可含 Path 引用或内联 base64）
	session   *session.Session
	runtime   *Runtime
	onEvent   map[events.ReactEventType][]func(data any)
	resultErr error

	// onAnyEvent 在执行循环发射每个 ReactEvent 时触发（先于类型特定处理器 onEvent）。
	// daemon 用它跟踪跨子智能体转发事件中的 agent_name。
	onAnyEvent []func(events.ReactEvent)

	// parentEmit 将 ReactEvent 转发到父级 EventBus。
	// 设置后（子智能体场景），本执行循环的所有事件也会发射到父级，
	// 实现跨智能体的事件可见性。
	parentEmit func(events.ReactEvent)

	// permissionCh 是子智能体授权冒泡的等待通道。
	// 子智能体场景下由 subAgentManager 在 spawn 时创建并注入：
	// 当本执行循环遇到需要授权的工具时（permission_pending），不立即终止循环，
	// 而是挂起等待主会话（用户）通过该通道送来的授权决策；
	// 授权到达后执行挂起工具并继续循环，实现「授权后子智能体继续执行」。
	// 主会话自身的执行循环不设置此通道，保持原有的「结束 → 下一轮魔法词恢复」行为。
	permissionCh chan permissionSignal

	// permissionSink 是子会话授权请求直达前端的旁路发送器。
	// 由宿主（如 mindx daemon）注入到派发 ctx，spawn 时复制到 builder：
	// 子会话触发授权时优先调用它发送授权请求，不依赖父 exec EventBus 的存活——
	// 父 exec 结束/被取消后其订阅销毁，原 parentEmit 转发链路会静默丢事件，
	// 导致前端收不到授权弹窗、子会话只能干等到 permission_timeout。
	// 未注入（如测试环境）时退回原 parentEmit 转发链路，行为保持不变。
	permissionSink PermissionSink

	resultAnswer            string
	resultUsage             session.TokenUsage
	resultIterations        int
	resultDuration          time.Duration
	resultTerminationReason string
}

// permissionSignal 承载外部（主会话）对子智能体权限请求的授权决策。
type permissionSignal struct {
	// action 为魔法词动作：tools.PermissionAllow / tools.PermissionAllowSession / tools.PermissionDeny。
	action string
}

func (b *AskBuilder) on(typ events.ReactEventType, fn func(data any)) *AskBuilder {
	b.onEvent[typ] = append(b.onEvent[typ], fn)
	return b
}

func (b *AskBuilder) OnThinking(fn func(chunk string)) *AskBuilder {
	return b.on(events.ThinkingDelta, func(d any) {
		if s, ok := d.(string); ok {
			fn(s)
		}
	})
}

func (b *AskBuilder) OnContent(fn func(chunk string)) *AskBuilder {
	return b.on(events.ContentDelta, func(d any) {
		if s, ok := d.(string); ok {
			fn(s)
		}
	})
}

func (b *AskBuilder) OnToolUseDelta(fn func(data events.ToolUseDeltaData)) *AskBuilder {
	return b.on(events.ToolUseDelta, func(d any) {
		if v, ok := d.(events.ToolUseDeltaData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnThinkingDone(fn func()) *AskBuilder {
	return b.on(events.ThinkingDone, func(d any) { fn() })
}

func (b *AskBuilder) OnToolStart(fn func(data events.ToolExecStartData)) *AskBuilder {
	return b.on(events.ToolExecStart, func(d any) {
		if v, ok := d.(events.ToolExecStartData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnToolEnd(fn func(data events.ToolExecEndData)) *AskBuilder {
	return b.on(events.ToolExecEnd, func(d any) {
		if v, ok := d.(events.ToolExecEndData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnSubtaskSpawned(fn func(data events.SubtaskInfo)) *AskBuilder {
	return b.on(events.SubtaskSpawned, func(d any) {
		if v, ok := d.(events.SubtaskInfo); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnSubtaskCompleted(fn func(data events.SubtaskResult)) *AskBuilder {
	return b.on(events.SubtaskCompleted, func(d any) {
		if v, ok := d.(events.SubtaskResult); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnPermissionRequest(fn func(data events.PermissionRequestData)) *AskBuilder {
	return b.on(events.PermissionRequest, func(d any) {
		if v, ok := d.(events.PermissionRequestData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnPermissionDenied(fn func(reason string)) *AskBuilder {
	return b.on(events.PermissionDenied, func(d any) {
		if s, ok := d.(string); ok {
			fn(s)
		}
	})
}

func (b *AskBuilder) OnAskUser(fn func(data events.AskUserRequestData)) *AskBuilder {
	return b.on(events.AskUserRequest, func(d any) {
		if v, ok := d.(events.AskUserRequestData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnAskUserPending(fn func(data events.AskUserPendingData)) *AskBuilder {
	return b.on(events.AskUserPending, func(d any) {
		if v, ok := d.(events.AskUserPendingData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnPermissionPending(fn func(data events.PermissionPendingData)) *AskBuilder {
	return b.on(events.PermissionPending, func(d any) {
		if v, ok := d.(events.PermissionPendingData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnExecutionSummary(fn func(data events.ExecutionSummaryData)) *AskBuilder {
	return b.on(events.ExecutionSummary, func(d any) {
		if v, ok := d.(events.ExecutionSummaryData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnCompaction(fn func(data events.CompactionData)) *AskBuilder {
	return b.on(events.Compaction, func(d any) {
		if v, ok := d.(events.CompactionData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnCompactStart(fn func(data events.CompactStartData)) *AskBuilder {
	return b.on(events.CompactStart, func(d any) {
		if v, ok := d.(events.CompactStartData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnCompactDone(fn func(data events.CompactDoneData)) *AskBuilder {
	return b.on(events.CompactDone, func(d any) {
		if v, ok := d.(events.CompactDoneData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnMicroCompactStart(fn func(data events.MicroCompactStartData)) *AskBuilder {
	return b.on(events.MicroCompactStart, func(d any) {
		if v, ok := d.(events.MicroCompactStartData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnMicroCompactDone(fn func(data events.MicroCompactDoneData)) *AskBuilder {
	return b.on(events.MicroCompactDone, func(d any) {
		if v, ok := d.(events.MicroCompactDoneData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnError(fn func(err string)) *AskBuilder {
	return b.on(events.Error, func(d any) {
		if s, ok := d.(string); ok {
			fn(s)
		}
	})
}

func (b *AskBuilder) OnLLMTimeout(fn func(data events.LLMTimeoutData)) *AskBuilder {
	return b.on(events.LLMTimeout, func(d any) {
		if v, ok := d.(events.LLMTimeoutData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnLLMCancelled(fn func(data events.LLMCancelledData)) *AskBuilder {
	return b.on(events.LLMCancelled, func(d any) {
		if v, ok := d.(events.LLMCancelledData); ok {
			fn(v)
		}
	})
}

// OnLLMRetry 注册 LLM 建流重试事件的处理器。
// 服务商限流（429）/ 5xx 等可预知错误触发退避重试时触发（Phase=retry），
// 重试成功建流后再次触发（Phase=recovered，前端应消除重试警告）。
// 重试耗尽仍失败时走 OnError 收尾。
func (b *AskBuilder) OnLLMRetry(fn func(data events.LLMRetryData)) *AskBuilder {
	return b.on(events.LLMRetry, func(d any) {
		if v, ok := d.(events.LLMRetryData); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnLoopEnd(fn func(data events.CycleInfo)) *AskBuilder {
	return b.on(events.LoopEnd, func(d any) {
		if v, ok := d.(events.CycleInfo); ok {
			fn(v)
		}
	})
}

func (b *AskBuilder) OnTaskSummary(fn func(data events.TaskSummaryData)) *AskBuilder {
	return b.on(events.TaskSummary, func(d any) {
		if v, ok := d.(events.TaskSummaryData); ok {
			fn(v)
		}
	})
}

// OnUserMessageSaved 注册一个处理器，在真实用户消息追加到会话后立即触发
// （魔法词不会被追加，因此不会触发）。处理器接收后端消息的 Timestamp，
// 前端将其存为 backendTimestamp，以支持对刚发送轮次的 session.delete_round
// 操作——甚至在会话重新加载之前即可执行。
func (b *AskBuilder) OnUserMessageSaved(fn func(data events.UserMessageSavedData)) *AskBuilder {
	return b.on(events.UserMessageSaved, func(d any) {
		if v, ok := d.(events.UserMessageSavedData); ok {
			fn(v)
		}
	})
}

// OnTokenUsageRecorded 注册每次 LLM 调用 token 用量更新的处理器。
// 在每次 LLM API 调用的 token 用量持久化到 TokenUsageStore 后触发。
// 用于 UI 中的实时 token 用量跟踪。
func (b *AskBuilder) OnTokenUsageRecorded(fn func(data session.TokenUsageRecord)) *AskBuilder {
	return b.on(events.TokenUsageRecorded, func(d any) {
		if v, ok := d.(session.TokenUsageRecord); ok {
			fn(v)
		}
	})
}

// OnEvent 注册一个全量处理器，在执行循环发射每个 ReactEvent 时触发
// （先于类型特定处理器）。处理器接收完整的 ReactEvent，包含 AgentName、
// SessionID、Type 和 Data。适用于跟踪「哪个智能体产生了事件」等元数据。
func (b *AskBuilder) OnEvent(fn func(events.ReactEvent)) *AskBuilder {
	b.onAnyEvent = append(b.onAnyEvent, fn)
	return b
}

// OnMaxTurnsReached 注册 MaxTurnsReached 事件的处理器。
// 当 Think-Act 循环达到 MaxTurns 仍未产生最终答案时触发。
// 这不是错误——而是正常的边界条件。
//
// 用于向用户展示友好的建议而非错误。示例：
//
//	result, err := rt.Ask("agent", "question", session).
//	    OnMaxTurnsReached(func(data events.MaxTurnsReachedData) {
//	        fmt.Println(data.Suggestion)
//	    }).
//	    Run()
func (b *AskBuilder) OnMaxTurnsReached(fn func(data events.MaxTurnsReachedData)) *AskBuilder {
	return b.on(events.MaxTurnsReached, func(d any) {
		if v, ok := d.(events.MaxTurnsReachedData); ok {
			fn(v)
		}
	})
}

// WithContext 为 AskBuilder 设置自定义 context。
// 若 context 被取消，执行循环将停止。
// Cancel() 不会取消自定义 context——调用方应自行取消。
func (b *AskBuilder) WithContext(ctx context.Context) *AskBuilder {
	b.ctx = ctx
	return b
}

// WithImages 为本次用户问题附加图片内容块。
// 每个块可为文件路径引用（Path，组装 LLM 消息时才读文件转 base64）
// 或内联数据（MediaType + Base64Data）。图片将随用户消息持久化，
// 并以 image_url 内容块进入 LLM 上下文。
func (b *AskBuilder) WithImages(imgs []session.ImageBlock) *AskBuilder {
	if len(imgs) > 0 {
		b.images = append(b.images, imgs...)
	}
	return b
}

// Cancel 停止执行循环。
func (b *AskBuilder) Cancel() {
	if b.cancel != nil {
		b.cancel()
	}
}

func (b *AskBuilder) OnAnswer(fn func(answer string)) *AskBuilder {
	return b.on(events.FinalAnswer, func(d any) {
		if s, ok := d.(string); ok {
			fn(s)
		}
	})
}

// Run 执行思考循环并返回结果。
func (b *AskBuilder) Run() (*RunResult, error) {
	b.runtime.exec(b)
	if b.resultErr != nil {
		reason := b.resultTerminationReason
		if reason == "" {
			reason = "error"
		}
		return &RunResult{
			Answer:            b.resultAnswer,
			TokenUsage:        b.resultUsage,
			Duration:          b.resultDuration,
			Iterations:        b.resultIterations,
			TerminationReason: reason,
		}, b.resultErr
	}
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
	}, nil
}
