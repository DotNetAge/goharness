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
}

// NewEventBus creates a new InProcessEventBus.
func NewEventBus() *InProcessEventBus {
	return &InProcessEventBus{
		subscribers: make(map[string]*subscriber),
	}
}

// Emit publishes an event to all active subscribers.
// Events that don't match a subscriber's filter are silently dropped.
// If a subscriber's channel is full, the event is dropped (non-blocking send)
// to prevent slow consumers from blocking the publisher.
func (b *InProcessEventBus) Emit(event core.ReactEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return
	}

	for _, sub := range b.subscribers {
		if sub.filter != nil && !sub.filter(event) {
			continue
		}
		select {
		case sub.ch <- event:
		default:
			// Channel full, drop event to avoid blocking publisher.
			// This is acceptable: UI consumers should have a buffered enough channel.
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
		return ch, func() {}
	}

	id := b.nextID
	b.nextID++

	sub := &subscriber{
		ch:     make(chan core.ReactEvent, StreamChannelBufferSize), // buffer for burst events
		filter: filter,
	}
	b.subscribers[idStr(id)] = sub

	unsubscribe := func() {
		b.mu.Lock()
		if sub, exists := b.subscribers[idStr(id)]; exists {
			delete(b.subscribers, idStr(id))
			close(sub.ch)
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
	for _, sub := range b.subscribers {
		close(sub.ch)
	}
	b.subscribers = make(map[string]*subscriber)
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
