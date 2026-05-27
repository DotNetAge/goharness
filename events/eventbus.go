package events

import (
	"fmt"
	"sync"

	"github.com/DotNetAge/goreact/logging"
)

const StreamChannelBufferSize = 256

type EventBus interface {
	Emit(event ReactEvent)
	Subscribe() (ch <-chan ReactEvent, cancel func())
	SubscribeFiltered(filter func(ReactEvent) bool) (ch <-chan ReactEvent, cancel func())
}

type subscriber struct {
	ch     chan ReactEvent
	filter func(ReactEvent) bool
}

type InProcessEventBus struct {
	mu          sync.RWMutex
	subscribers map[string]*subscriber
	closed      bool
	nextID      int
	logger      logging.Logger
}

func (b *InProcessEventBus) SetLogger(logger logging.Logger) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.logger = logger
}

func NewEventBus() *InProcessEventBus {
	return &InProcessEventBus{
		subscribers: make(map[string]*subscriber),
	}
}

func isCriticalEvent(eventType ReactEventType) bool {
	return eventType == PermissionRequest || eventType == PermissionDenied
}

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

func (b *InProcessEventBus) Subscribe() (<-chan ReactEvent, func()) {
	return b.SubscribeFiltered(nil)
}

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

func (b *InProcessEventBus) SubscriberCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subscribers)
}
