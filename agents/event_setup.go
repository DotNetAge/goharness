package agents

import (
	"context"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
)

// prepareEventBus 搭建 exec 的事件总线：创建 EventBus、注册事件分发回调、构造 emit 闭包、
// 将父级发射器注入 ctx，并注册会话压缩事件处理器。
//
// 返回：
//   - emit:     向 EventBus 发射事件的闭包（自动补齐 SessionID/AgentName/Timestamp）
//   - emitRaw:  直接向 EventBus 发射已构造好的 ReactEvent，供工具执行器等
//     需要发射完整事件的调用方使用
//   - outCtx:   注入了父级发射器的新 context（供子智能体 subAgents.spawn 获取并转发事件）
//   - cleanup:  取消 EventBus 订阅，exec 退出时调用
//
// conversationID 与 totalUsage 由 exec 自身维护（依赖循环状态），不在此函数职责内。
func prepareEventBus(b *AskBuilder, logger logging.Logger, ctx context.Context) (
	emit func(events.ReactEventType, any),
	emitRaw func(events.ReactEvent),
	outCtx context.Context,
	cleanup func(),
) {
	sid := b.session.ID()
	eb := events.NewEventBus()
	eb.SetLogger(logger)
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

	emit = func(typ events.ReactEventType, data any) {
		eb.Emit(events.ReactEvent{
			SessionID: sid,
			AgentName: b.agentName,
			Type:      typ,
			Data:      data,
			Timestamp: time.Now().UnixMilli(),
		})
	}

	// emitRaw 直接向 EventBus 发射已构造好的事件，供工具执行器等需要发射完整
	// ReactEvent 的调用方使用（emit 会补齐 SessionID/AgentName/Timestamp 元信息）。
	emitRaw = func(ev events.ReactEvent) { eb.Emit(ev) }

	// 将父级 EventBus 发射器存入 context，供子智能体（subAgents.spawn）获取并转发事件。
	outCtx = context.WithValue(ctx, parentEmitKeyType{}, emitRaw)

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
			MaxWindowSize: b.session.ModelContextLength(),
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
			MaxWindowSize: b.session.ModelContextLength(),
			Ratio:         ratio,
		})
	})

	return emit, emitRaw, outCtx, cancel
}
