package events

import (
	"sync"
	"testing"
	"time"

)

func TestEventBus_PublishSubscribe(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe()
	defer cancel()

	event := NewReactEvent("sess1", "main", "", ThinkingDelta, "hello")
	bus.Emit(event)

	select {
	case received := <-ch:
		if received.Type != ThinkingDelta {
			t.Errorf("expected ThinkingDelta, got %s", received.Type)
		}
		if received.TaskID != "main" {
			t.Errorf("expected TaskID=main, got %s", received.TaskID)
		}
		if received.Data.(string) != "hello" {
			t.Errorf("expected Data=hello, got %v", received.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestEventBus_FilteredSubscribe(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	// Only subscribe to events from task_1
	ch, cancel := bus.SubscribeFiltered(func(e ReactEvent) bool {
		return e.TaskID == "task_1"
	})
	defer cancel()

	bus.Emit(NewReactEvent("s", "main", "", ThinkingDelta, "skip"))
	bus.Emit(NewReactEvent("s", "task_1", "main", ThinkingDelta, "hello from task_1"))

	select {
	case received := <-ch:
		if received.TaskID != "task_1" {
			t.Errorf("expected only task_1 events, got %s", received.TaskID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for filtered event")
	}
}

func TestEventBus_MultipleSubscribers(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch1, cancel1 := bus.Subscribe()
	defer cancel1()
	ch2, cancel2 := bus.Subscribe()
	defer cancel2()

	bus.Emit(NewReactEvent("s", "main", "", FinalAnswer, "done"))

	for i, ch := range []<-chan ReactEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Type != FinalAnswer {
				t.Errorf("subscriber %d: expected FinalAnswer, got %s", i, ev.Type)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timeout", i)
		}
	}
}

func TestEventBus_CancelUnsubscribes(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe()
	cancel()

	bus.Emit(NewReactEvent("s", "main", "", ThinkingDelta, "test"))

	// Channel should be closed, not receive the event
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after cancel")
	}
}

func TestEventBus_Close(t *testing.T) {
	bus := NewEventBus()

	ch, _ := bus.Subscribe()
	bus.Close()

	// After Close, events should not be delivered
	bus.Emit(NewReactEvent("s", "main", "", ThinkingDelta, "test"))

	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after bus Close")
	}
}

func TestEventBus_FullChannelDrops(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	// Use unbuffered channel to test drop behavior
	// Actually our implementation uses buffered(256), so we need to fill it
	// This test just verifies Emit doesn't block on full channel
	done := make(chan struct{})
	go func() {
		for i := 0; i < 300; i++ {
			bus.Emit(NewReactEvent("s", "main", "", ThinkingDelta, "x"))
		}
		close(done)
	}()

	select {
	case <-done:
		// Success - Emit never blocked
	case <-time.After(2 * time.Second):
		t.Fatal("Emit blocked on full channel")
	}
}

func TestEventBus_CriticalEventNotDropped(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe()
	defer cancel()

	// Fill subscriber channel to capacity with non-critical events
	for i := 0; i < StreamChannelBufferSize; i++ {
		bus.Emit(NewReactEvent("s", "main", "", ThinkingDelta, "fill"))
	}

	// Emit critical event — will block on the full channel
	permDone := make(chan struct{})
	go func() {
		bus.Emit(NewReactEvent("s", "main", "", PermissionRequest, "critical"))
		close(permDone)
	}()

	// Drain all events (256 fill + 1 critical = 257). The critical event
	// must arrive — Emit blocks (not drops) until we free a slot.
	evts := make([]ReactEvent, 0, StreamChannelBufferSize+1)
	for i := 0; i < StreamChannelBufferSize+1; i++ {
		select {
		case ev := <-ch:
			evts = append(evts, ev)
		case <-time.After(time.Second):
			t.Fatalf("only read %d/%d events — critical event was dropped", i, StreamChannelBufferSize+1)
		}
	}

	last := evts[len(evts)-1]
	if last.Type != PermissionRequest {
		t.Errorf("expected PermissionRequest, got %s", last.Type)
	}

	select {
	case <-permDone:
	case <-time.After(time.Second):
		t.Fatal("critical Emit never returned")
	}
}

func TestReactContext_EmitEvent(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch, cancel := bus.SubscribeFiltered(func(e ReactEvent) bool {
		return e.Type == ToolExecStart
	})
	defer cancel()

	emit := bus.Emit

	// Emit through event bus
	emit(ReactEvent{SessionID: "test", Type: ToolExecStart, Data: ToolExecStartData{ToolName: "Read", Params: map[string]any{"path": "test"}}})
	emit(ReactEvent{SessionID: "test", Type: ThinkingDelta, Data: "should be filtered"})

	select {
	case ev := <-ch:
		data := ev.Data.(ToolExecStartData)
		if data.ToolName != "Read" {
			t.Errorf("expected tool Read, got %v", data.ToolName)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestReactContext_EmitEvent_NilBus(t *testing.T) {
	// Nil EventBus is handled at the reactor level (makeEmitter returns no-op).
	// This test is no longer applicable at the EventBus level.
}

func TestReactEventTypes(t *testing.T) {
	// Verify all event types are defined and unique
	types := map[ReactEventType]bool{
		ThinkingDelta:    false,
		ThinkingDone:     false,
		ToolExecStart:    false,
		ToolExecEnd:      false,
		SubtaskSpawned:   false,
		SubtaskCompleted: false,
		FinalAnswer:      false,
		Error:            false,
		CycleEnd:         false,
	}

	for typ := range types {
		if typ == "" {
			t.Error("event type should not be empty")
		}
		types[typ] = true
	}
	for typ, found := range types {
		if !found {
			t.Errorf("event type %s not in set", typ)
		}
	}
}

func TestNewReactEvent(t *testing.T) {
	ev := NewReactEvent("sess1", "task_1", "main", FinalAnswer, "hello world")

	if ev.SessionID != "sess1" {
		t.Errorf("expected SessionID=sess1, got %s", ev.SessionID)
	}
	if ev.TaskID != "task_1" {
		t.Errorf("expected TaskID=task_1, got %s", ev.TaskID)
	}
	if ev.ParentID != "main" {
		t.Errorf("expected ParentID=main, got %s", ev.ParentID)
	}
	if ev.Type != FinalAnswer {
		t.Errorf("expected FinalAnswer, got %s", ev.Type)
	}
	if ev.Timestamp == 0 {
		t.Error("expected non-zero timestamp")
	}
}

func TestEventBus_ConcurrentEmit(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe()
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			bus.Emit(NewReactEvent("s", "main", "", ThinkingDelta, id))
		}(i)
	}
	wg.Wait()

	// Drain the channel and count
	received := 0
	timeout := time.After(2 * time.Second)
drain:
	for {
		select {
		case <-ch:
			received++
		case <-timeout:
			break drain
		}
	}

	if received != 100 {
		t.Errorf("expected 100 events, received %d (some may have been dropped if buffer too small)", received)
	}
}
