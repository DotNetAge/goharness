// Package events provides an event bus implementation for publishing and subscribing to React events.
package events

import (
	"fmt"
	"sync"

	"github.com/DotNetAge/goreact/logging"
)

const (
	// StreamChannelBufferSize defines the buffer size for subscriber channels.
	StreamChannelBufferSize = 256
)

// EventBus defines the interface for an event publishing and subscription system.
// It allows emitting events and subscribing to them with optional filtering.
type EventBus interface {
	// Emit publishes an event to all subscribers.
	Emit(event ReactEvent)
	// Subscribe returns a channel of events and a cancellation function to unsubscribe.
	Subscribe() (ch <-chan ReactEvent, cancel func())
	// SubscribeFiltered returns a channel of filtered events and a cancellation function.
	SubscribeFiltered(filter func(ReactEvent) bool) (ch <-chan ReactEvent, cancel func())
}

// subscriber represents a single subscriber with its channel and optional filter.
type subscriber struct {
	ch     chan ReactEvent
	filter func(ReactEvent) bool
}

// InProcessEventBus is an in-process implementation of EventBus using channels.
// It supports filtered subscriptions and graceful shutdown.
type InProcessEventBus struct {
	mu          sync.RWMutex
	subscribers map[string]*subscriber
	closed      bool
	nextID      int
	logger      logging.Logger
}

// SetLogger sets the logger for the event bus.
func (b *InProcessEventBus) SetLogger(logger logging.Logger) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logger = logger
}

// NewEventBus creates a new InProcessEventBus instance.
func NewEventBus() *InProcessEventBus {
	return &InProcessEventBus{
		subscribers: make(map[string]*subscriber),
	}
}

// isCriticalEvent checks if an event type requires guaranteed delivery.
func isCriticalEvent(eventType ReactEventType) bool {
	return eventType == PermissionRequest || eventType == PermissionDenied
}

// Emit publishes an event to all matching subscribers.
// Critical events (permission-related) are delivered synchronously;
// non-critical events use non-blocking sends to avoid slow consumer backpressure.
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
			}
		}
	}
}

// Subscribe subscribes to all events on the bus.
// Returns a receive-only channel and a cancel function to unsubscribe.
func (b *InProcessEventBus) Subscribe() (<-chan ReactEvent, func()) {
	return b.SubscribeFiltered(nil)
}

// SubscribeFiltered subscribes to events matching the provided filter function.
// If filter is nil, all events are received. Returns a channel and cancel function.
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

// Close shuts down the event bus, closing all subscriber channels.
// Subsequent Emit and Subscribe calls will be no-ops.
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
