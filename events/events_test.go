package events

import (
	"sync"
	"testing"
	"time"
)

func TestNewEventBus(t *testing.T) {
	bus := NewEventBus()
	if bus == nil {
		t.Fatal("NewEventBus() returned nil")
	}
	if bus.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers, got %d", bus.SubscriberCount())
	}
}

func TestSubscribeAndEmit(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe()
	defer cancel()

	event := NewReactEvent("session-1", "task-1", "", ThinkingDone, "test data")
	bus.Emit(event)

	select {
	case received := <-ch:
		if received.Type != ThinkingDone {
			t.Errorf("expected event type %s, got %s", ThinkingDone, received.Type)
		}
		if received.SessionID != "session-1" {
			t.Errorf("expected session ID %s, got %s", "session-1", received.SessionID)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("timed out waiting for event")
	}
}

func TestMultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch1, cancel1 := bus.Subscribe()
	defer cancel1()

	ch2, cancel2 := bus.Subscribe()
	defer cancel2()

	event := NewReactEvent("session-1", "task-1", "", ContentDelta, "hello")
	bus.Emit(event)

	for i, ch := range []<-chan ReactEvent{ch1, ch2} {
		select {
		case received := <-ch:
			if received.Type != ContentDelta {
				t.Errorf("subscriber %d: expected event type %s, got %s", i, ContentDelta, received.Type)
			}
		case <-time.After(100 * time.Millisecond):
			t.Fatalf("subscriber %d: timed out waiting for event", i)
		}
	}
}

func TestUnsubscribe(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe()

	event := NewReactEvent("session-1", "task-1", "", ToolExecStart, nil)
	bus.Emit(event)

	select {
	case <-ch:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("should receive event before unsubscribe")
	}

	cancel()

	if bus.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers after unsubscribe, got %d", bus.SubscriberCount())
	}

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel should be closed after unsubscribe")
		}
	default:
	}
}

func TestFilteredSubscription(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	filteredCh, filteredCancel := bus.SubscribeFiltered(func(e ReactEvent) bool {
		return e.Type == ToolExecStart || e.Type == ToolExecEnd
	})
	defer filteredCancel()

	_, allCancel := bus.Subscribe()
	defer allCancel()

	events := []ReactEvent{
		NewReactEvent("s1", "t1", "", ToolExecStart, nil),
		NewReactEvent("s1", "t1", "", ContentDelta, "data"),
		NewReactEvent("s1", "t1", "", ToolExecEnd, nil),
	}

	for _, ev := range events {
		bus.Emit(ev)
	}

	time.Sleep(50 * time.Millisecond)

	var filteredReceived []ReactEventType
drainFiltered:
	for {
		select {
		case ev := <-filteredCh:
			filteredReceived = append(filteredReceived, ev.Type)
		default:
			break drainFiltered
		}
	}

	if len(filteredReceived) != 2 {
		t.Fatalf("expected 2 filtered events, got %d", len(filteredReceived))
	}
	if filteredReceived[0] != ToolExecStart {
		t.Errorf("first filtered event should be ToolExecStart, got %s", filteredReceived[0])
	}
	if filteredReceived[1] != ToolExecEnd {
		t.Errorf("second filtered event should be ToolExecEnd, got %s", filteredReceived[1])
	}
}

func TestCloseBus(t *testing.T) {
	bus := NewEventBus()

	ch1, _ := bus.Subscribe()
	ch2, _ := bus.Subscribe()

	if bus.SubscriberCount() != 2 {
		t.Errorf("expected 2 subscribers, got %d", bus.SubscriberCount())
	}

	bus.Close()

	if bus.SubscriberCount() != 0 {
		t.Errorf("expected 0 subscribers after close, got %d", bus.SubscriberCount())
	}

	for i, ch := range []<-chan ReactEvent{ch1, ch2} {
		select {
		case _, ok := <-ch:
			if ok {
				t.Errorf("channel %d should be closed after Close()", i)
			}
		default:
			t.Errorf("channel %d should be closed but is not", i)
		}
	}
}

func TestEmitAfterClose(t *testing.T) {
	bus := NewEventBus()

	ch, cancel := bus.Subscribe()
	defer cancel()

	bus.Close()

	event := NewReactEvent("s1", "t1", "", Error, "test")
	bus.Emit(event)

	select {
	case received, ok := <-ch:
		if ok {
			t.Errorf("should not receive events after close, got: %+v", received)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func TestSubscribeAfterClose(t *testing.T) {
	bus := NewEventBus()
	bus.Close()

	ch, cancel := bus.Subscribe()
	defer cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("channel from Subscribe after Close should be closed immediately")
		}
	default:
		t.Error("channel from Subscribe after Close should be closed immediately")
	}

	_ = cancel
}

func TestConcurrentEmit(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe()
	defer cancel()

	var wg sync.WaitGroup
	const goroutines = 10
	const eventsPerGoroutine = 20

	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				event := NewReactEvent("s1", "t1", "", ContentDelta,
					map[string]interface{}{"goroutine": id, "index": i},
				)
				bus.Emit(event)
			}
		}(g)
	}

	wg.Wait()

	time.Sleep(100 * time.Millisecond)

	receivedCount := 0
drain:
	for {
		select {
		case <-ch:
			receivedCount++
		default:
			break drain
		}
	}

	if receivedCount == 0 {
		t.Error("should have received at least some events")
	}
}

func TestCriticalEventsAlwaysDelivered(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	smallCh, cancel := bus.SubscribeFiltered(nil)
	defer cancel()

	var receivedCritical bool
	go func() {
		for event := range smallCh {
			if event.Type == PermissionRequest {
				receivedCritical = true
			}
		}
	}()

	for i := 0; i < StreamChannelBufferSize+10; i++ {
		bus.Emit(NewReactEvent("s1", "t1", "", ContentDelta, "fill"))
	}

	criticalEvent := NewReactEvent("s1", "t1", "", PermissionRequest, "critical")
	bus.Emit(criticalEvent)

	time.Sleep(100 * time.Millisecond)

	if !receivedCritical {
		t.Error("critical event should be delivered even when channel is busy")
	}
}

func TestSubscriberCount(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	if count := bus.SubscriberCount(); count != 0 {
		t.Errorf("initial count should be 0, got %d", count)
	}

	_, cancel1 := bus.Subscribe()
	if count := bus.SubscriberCount(); count != 1 {
		t.Errorf("after 1 subscribe, count should be 1, got %d", count)
	}

	_, cancel2 := bus.Subscribe()
	if count := bus.SubscriberCount(); count != 2 {
		t.Errorf("after 2 subscribes, count should be 2, got %d", count)
	}

	cancel1()
	if count := bus.SubscriberCount(); count != 1 {
		t.Errorf("after 1 unsubscribe, count should be 1, got %d", count)
	}

	cancel2()
	if count := bus.SubscriberCount(); count != 0 {
		t.Errorf("after all unsubscribes, count should be 0, got %d", count)
	}
}
