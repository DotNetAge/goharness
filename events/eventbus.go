// Package events 提供了一个事件总线实现，用于发布和订阅 React 事件。
package events

import (
	"fmt"
	"sync"

	"github.com/DotNetAge/goharness/logging"
)

const (
	// StreamChannelBufferSize 定义订阅者通道的缓冲区大小。
	// 从 256 增加到 8192，以防止在子代理突发流量
	// （思考/内容增量、工具事件等）下发生事件丢失。
	StreamChannelBufferSize = 8192
)

// EventBus 定义了事件发布和订阅系统的接口。
// 它允许发出事件并可选地通过过滤条件订阅事件。
type EventBus interface {
	// Emit 向所有订阅者发布一个事件。
	Emit(event ReactEvent)
	// Subscribe 返回一个事件通道和一个用于取消订阅的取消函数。
	Subscribe() (ch <-chan ReactEvent, cancel func())
	// SubscribeFiltered 返回一个过滤后的事件通道和一个取消函数。
	SubscribeFiltered(filter func(ReactEvent) bool) (ch <-chan ReactEvent, cancel func())
}

// subscriber 表示单个订阅者，包含其通道和可选的过滤器。
type subscriber struct {
	ch     chan ReactEvent
	filter func(ReactEvent) bool
}

// InProcessEventBus 是基于通道实现的进程内 EventBus。
// 它支持过滤订阅和优雅关闭。
type InProcessEventBus struct {
	mu          sync.RWMutex
	subscribers map[string]*subscriber
	closed      bool
	nextID      int
	logger      logging.Logger
}

// SetLogger 设置事件总线的日志记录器。
func (b *InProcessEventBus) SetLogger(logger logging.Logger) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logger = logger
}

// NewEventBus 创建一个新的 InProcessEventBus 实例。
func NewEventBus() *InProcessEventBus {
	return &InProcessEventBus{
		subscribers: make(map[string]*subscriber),
	}
}

// isCriticalEvent 检查事件类型是否需要保证投递。
// 关键事件使用阻塞式（同步）通道写入发送。
// 非关键事件使用非阻塞写入，当订阅者通道已满时会静默丢弃
// （背压安全阀）。
//
// 任何携带 UI 所依赖的流式内容或执行结果的事件类型
// 都必须标记为关键事件。
func isCriticalEvent(eventType ReactEventType) bool {
	switch eventType {
	case PermissionRequest, PermissionDenied,
		ThinkingDelta, ContentDelta, ThinkingDone,
		ToolExecStart, ToolExecEnd, ToolUseDelta,
		FinalAnswer, TaskSummary,
		Error, MaxTurnsReached, LLMTimeout, LLMCancelled, LLMRetry,
		SubtaskSpawned, SubtaskCompleted:
		return true
	}
	return false
}

// Emit 向所有匹配的订阅者发布事件。
// 关键事件（权限相关）以同步方式投递；
// 非关键事件使用非阻塞发送，以避免慢消费者造成的背压。
func (b *InProcessEventBus) Emit(event ReactEvent) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		return
	}

	subs := make([]*subscriber, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		if sub.filter != nil && !sub.filter(event) {
			continue
		}
		subs = append(subs, sub)
	}
	b.mu.RUnlock()

	for _, sub := range subs {
		if isCriticalEvent(event.Type) {
			sub.ch <- event
		} else {
			select {
			case sub.ch <- event:
			default:
				if b.logger != nil {
					b.logger.Warn("[eventbus] non-critical event dropped — subscriber channel full",
						"event_type", event.Type,
						"agent", event.AgentName,
						"session", event.SessionID,
					)
				}
			}
		}
	}
}

// Subscribe 订阅总线上的所有事件。
// 返回一个只读通道和一个用于取消订阅的取消函数。
func (b *InProcessEventBus) Subscribe() (<-chan ReactEvent, func()) {
	return b.SubscribeFiltered(nil)
}

// SubscribeFiltered 订阅与提供的过滤函数匹配的事件。
// 如果 filter 为 nil，则接收所有事件。返回一个通道和取消函数。
func (b *InProcessEventBus) SubscribeFiltered(filter func(ReactEvent) bool) (<-chan ReactEvent, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		ch := make(chan ReactEvent)
		close(ch)
		if b.logger != nil {
			b.logger.Warn("[eventbus] subscribe failed — bus closed")
		}
		return ch, func() {}
	}

	id := b.nextID
	b.nextID++
	idStr := fmt.Sprintf("%d", id)

	sub := &subscriber{
		ch:     make(chan ReactEvent, StreamChannelBufferSize),
		filter: filter,
	}
	b.subscribers[idStr] = sub

	if b.logger != nil {
		b.logger.Debug("[eventbus] subscriber added", "id", idStr, "buffer_cap", StreamChannelBufferSize)
	}

	unsubscribe := func() {
		b.mu.Lock()
		if sub, exists := b.subscribers[idStr]; exists {
			delete(b.subscribers, idStr)
			close(sub.ch)
			if b.logger != nil {
				b.logger.Debug("[eventbus] subscriber removed", "id", idStr)
			}
		}
		b.mu.Unlock()
	}

	return sub.ch, unsubscribe
}

// Close 关闭事件总线，关闭所有订阅者通道。
// 后续的 Emit 和 Subscribe 调用将变为无操作。
func (b *InProcessEventBus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.closed = true
	count := len(b.subscribers)
	for id, sub := range b.subscribers {
		close(sub.ch)
		if b.logger != nil {
			b.logger.Debug("[eventbus] closing subscriber", "id", id)
		}
	}
	b.subscribers = make(map[string]*subscriber)
	if b.logger != nil {
		b.logger.Debug("[eventbus] bus closed", "subscribers_closed", count)
	}
}

// SubscriberCount 返回当前活跃订阅者的数量。
func (b *InProcessEventBus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
