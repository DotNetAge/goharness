package reactor

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DotNetAge/goreact/core"
)

func TestEventBus_PublishSubscribe(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch, cancel := bus.Subscribe()
	defer cancel()

	event := core.NewReactEvent("sess1", "main", "", core.ThinkingDelta, "hello")
	bus.Emit(event)

	select {
	case received := <-ch:
		if received.Type != core.ThinkingDelta {
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
	ch, cancel := bus.SubscribeFiltered(func(e core.ReactEvent) bool {
		return e.TaskID == "task_1"
	})
	defer cancel()

	bus.Emit(core.NewReactEvent("s", "main", "", core.ThinkingDelta, "skip"))
	bus.Emit(core.NewReactEvent("s", "task_1", "main", core.ActionStart, core.ActionStartData{ToolCount: 1, ToolNames: []string{"Grep"}}))

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

	bus.Emit(core.NewReactEvent("s", "main", "", core.FinalAnswer, "done"))

	for i, ch := range []<-chan core.ReactEvent{ch1, ch2} {
		select {
		case ev := <-ch:
			if ev.Type != core.FinalAnswer {
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

	bus.Emit(core.NewReactEvent("s", "main", "", core.ThinkingDelta, "test"))

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
	bus.Emit(core.NewReactEvent("s", "main", "", core.ThinkingDelta, "test"))

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
			bus.Emit(core.NewReactEvent("s", "main", "", core.ThinkingDelta, "x"))
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

func TestReactContext_EmitEvent(t *testing.T) {
	bus := NewEventBus()
	defer bus.Close()

	ch, cancel := bus.SubscribeFiltered(func(e core.ReactEvent) bool {
		return e.Type == core.ActionStart
	})
	defer cancel()

	ctx := NewReactContextWithIDs(context.Background(), "main", "", "test input", nil, 10)
	ctx.emitEvent = bus.Emit

	// Emit through context
	ctx.EmitEvent(core.ActionStart, core.ActionStartData{ToolCount: 1, ToolNames: []string{"Read"}})
	ctx.EmitEvent(core.ThinkingDelta, "should be filtered")

	select {
	case ev := <-ch:
		data := ev.Data.(core.ActionStartData)
		if len(data.ToolNames) != 1 || data.ToolNames[0] != "Read" {
			t.Errorf("expected tool Read, got %v", data.ToolNames)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestReactContext_EmitEvent_NilBus(t *testing.T) {
	// Should not panic when emitEvent is nil
	ctx := NewReactContext(context.Background(), "test", nil, 10)
	ctx.emitEvent = nil // explicitly nil
	ctx.EmitEvent(core.ActionStart, "test") // should be no-op
}

func TestReactEventTypes(t *testing.T) {
	// Verify all event types are defined and unique
	types := map[core.ReactEventType]bool{
		core.ThinkingDelta:   false,
		core.ThinkingDone:    false,
		core.ActionStart:     false,
		core.ActionProgress:  false,
		core.ToolExecStart:   false,
		core.ToolExecEnd:     false,
		core.ActionEnd:       false,
		core.ObservationDone: false,
		core.SubtaskSpawned:  false,
		core.SubtaskCompleted: false,
		core.FinalAnswer:     false,
		core.ClarifyNeeded:   false,
		core.Error:           false,
		core.CycleEnd:        false,
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
	ev := core.NewReactEvent("sess1", "task_1", "main", core.FinalAnswer, "hello world")

	if ev.SessionID != "sess1" {
		t.Errorf("expected SessionID=sess1, got %s", ev.SessionID)
	}
	if ev.TaskID != "task_1" {
		t.Errorf("expected TaskID=task_1, got %s", ev.TaskID)
	}
	if ev.ParentID != "main" {
		t.Errorf("expected ParentID=main, got %s", ev.ParentID)
	}
	if ev.Type != core.FinalAnswer {
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
			bus.Emit(core.NewReactEvent("s", "main", "", core.ThinkingDelta, id))
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
