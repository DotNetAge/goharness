package reactor

import (
	"fmt"
	"sync"

	"github.com/DotNetAge/goreact/core"
)

// EventBus is the interface for publishing and subscribing to ReactEvents.
// It decouples the Reactor's internal T-A-O loop from external consumers (clients, UI).
type EventBus interface {
	// Emit publishes an event to all subscribers.
	Emit(event core.ReactEvent)

	// Subscribe returns a channel that receives all published events.
	// The returned cancel function stops the subscription and closes the channel.
	Subscribe() (ch <-chan core.ReactEvent, cancel func())

	// SubscribeFiltered returns a channel that only receives events matching the filter.
	SubscribeFiltered(filter func(core.ReactEvent) bool) (ch <-chan core.ReactEvent, cancel func())
}

// subscriber represents a single subscriber with its filter and cancel state.
type subscriber struct {
	ch     chan core.ReactEvent
	filter func(core.ReactEvent) bool // nil = no filter, receive all
}

// InProcessEventBus is an in-process EventBus implementation using fan-out channels.
// It is safe for concurrent use from multiple goroutines (e.g., main reactor + subagents).
type InProcessEventBus struct {
	mu          sync.RWMutex
	subscribers map[string]*subscriber
	closed      bool
	nextID      int
	logger      core.Logger // optional: set via SetLogger for observability
}

// SetLogger attaches a logger for tracing subscribe/unsubscribe/emit/drop events.
func (b *InProcessEventBus) SetLogger(logger core.Logger) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logger = logger
}

// NewEventBus creates a new InProcessEventBus.
func NewEventBus() *InProcessEventBus {
	return &InProcessEventBus{
		subscribers: make(map[string]*subscriber),
	}
}

// isCriticalEvent returns true for event types that MUST NOT be dropped.
// These events carry permission decisions that block tool execution.
func isCriticalEvent(eventType core.ReactEventType) bool {
	return eventType == core.PermissionRequest || eventType == core.PermissionDenied
}

// emitTargets is the snapshot of subscribers collected under lock, sent without lock held.
type emitTarget struct {
	ch     chan core.ReactEvent
	filter func(core.ReactEvent) bool
}

// Emit publishes an event to all active subscribers.
//
// Non-critical events: non-blocking send with silent drop on full channel.
// Critical events (PermissionRequest, PermissionDenied): blocking send
// — guarantees delivery to prevent tool-execution hang.
//
// The subscribers map is snapshotted under RLock, then the lock is released
// before any channel send, eliminating deadlock risk between Emit and unsubscribe/Close.
func (b *InProcessEventBus) Emit(event core.ReactEvent) {
	b.mu.RLock()
	if b.closed {
		b.mu.RUnlock()
		if b.logger != nil && event.Type != core.ThinkingDelta {
			b.logger.Debug("[eventbus] emit skipped — bus closed", "type", event.Type, "session_id", event.SessionID)
		}
		return
	}

	if b.logger != nil && event.Type != core.ThinkingDelta {
		b.logger.Debug("[eventbus] emit", "type", event.Type, "session_id", event.SessionID, "subscribers", len(b.subscribers))
	}

	targets := make([]emitTarget, 0, len(b.subscribers))
	for _, sub := range b.subscribers {
		if sub.filter != nil && !sub.filter(event) {
			continue
		}
		targets = append(targets, emitTarget{ch: sub.ch, filter: sub.filter})
	}
	b.mu.RUnlock()

	if isCriticalEvent(event.Type) {
		for _, t := range targets {
			t.ch <- event
		}
		return
	}

	for _, t := range targets {
		select {
		case t.ch <- event:
		default:
			if b.logger != nil {
				b.logger.Warn("[eventbus] event dropped — subscriber channel full", "type", event.Type, "session_id", event.SessionID)
			}
		}
	}
}

// Subscribe returns a read-only channel of all events and a cancel function.
func (b *InProcessEventBus) Subscribe() (<-chan core.ReactEvent, func()) {
	return b.SubscribeFiltered(nil)
}

// SubscribeFiltered returns a read-only channel of filtered events and a cancel function.
func (b *InProcessEventBus) SubscribeFiltered(filter func(core.ReactEvent) bool) (<-chan core.ReactEvent, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		ch := make(chan core.ReactEvent)
		close(ch)
		if b.logger != nil {
			b.logger.Warn("[eventbus] subscribe failed — bus closed")
		}
		return ch, func() {}
	}

	id := b.nextID
	b.nextID++
	idStr := idStr(id)

	sub := &subscriber{
		ch:     make(chan core.ReactEvent, StreamChannelBufferSize), // buffer for burst events
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

// Close shuts down the event bus, closing all subscriber channels.
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

// SubscriberCount returns the current number of active subscribers.
func (b *InProcessEventBus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}

// idStr converts an integer subscriber ID to its string representation for use as a map key.
func idStr(n int) string {
	return fmt.Sprintf("%d", n)
}
