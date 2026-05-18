package reactor

import (
	"testing"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// ===========================================================================
// AgentMode Tests
// ===========================================================================

func TestAgentMode_String(t *testing.T) {
	tests := []struct {
		mode AgentMode
		want string
	}{
		{ModeExecutor, "executor"},
		{ModeCoordinator, "coordinator"},
	}

	for _, tt := range tests {
		if got := tt.mode.String(); got != tt.want {
			t.Errorf("AgentMode(%q).String() = %q, want %q", tt.mode, got, tt.want)
		}
	}
}

func TestAgentMode_IsExecutor_IsCoordinator(t *testing.T) {
	if !ModeExecutor.IsExecutor() {
		t.Error("ModeExecutor.IsExecutor() = false")
	}
	if ModeExecutor.IsCoordinator() {
		t.Error("ModeExecutor.IsCoordinator() = true")
	}
	if ModeCoordinator.IsExecutor() {
		t.Error("ModeCoordinator.IsExecutor() = true")
	}
	if !ModeCoordinator.IsCoordinator() {
		t.Error("ModeCoordinator.IsCoordinator() = false")
	}
}

// ===========================================================================
// LifecycleState Tests
// ===========================================================================

func TestLifecycleState_IsTerminal(t *testing.T) {
	terminalStates := []LifecycleState{
		LifecycleCancelled,
		LifecycleCompleted,
	}
	nonTerminal := []LifecycleState{
		LifecycleRunning,
		LifecycleInterrupted,
	}

	for _, s := range terminalStates {
		if !s.IsTerminal() {
			t.Errorf("%v should be terminal", s)
		}
	}
	for _, s := range nonTerminal {
		if s.IsTerminal() {
			t.Errorf("%v should NOT be terminal", s)
		}
	}
}

func TestLifecycleState_CanTransitionTo(t *testing.T) {
	validTransitions := map[LifecycleState][]LifecycleState{
		LifecycleRunning:    {LifecycleInterrupted, LifecycleCancelled, LifecycleCompleted},
		LifecycleInterrupted: {LifecycleRunning, LifecycleCancelled},
	}

	for from, targets := range validTransitions {
		for _, to := range targets {
			if !from.CanTransitionTo(to) {
				t.Errorf("%v → %v: expected valid transition", from, to)
			}
		}
	}

	// Invalid transitions
	if LifecycleRunning.CanTransitionTo(LifecycleRunning) {
		t.Error("Running → Running should be invalid")
	}
	if LifecycleCancelled.CanTransitionTo(LifecycleRunning) {
		t.Error("Cancelled → Running should be invalid (terminal)")
	}
	if LifecycleCompleted.CanTransitionTo(LifecycleRunning) {
		t.Error("Completed → Running should be invalid (terminal)")
	}
}

// ===========================================================================
// TaskProgressTable Tests
// ===========================================================================

func TestTaskProgressTable_AddAndGet(t *testing.T) {
	table := NewTaskProgressTable("parent-1")
	entry := &TaskEntry{
		TaskID:      "task-1",
		Title:       "Write code",
		Description: "Implement feature X",
		Priority:    1,
		Status:      TaskDispatched,
		MaxRetries:  3,
	}

	table.Add(entry)

	got := table.Get("task-1")
	if got == nil {
		t.Fatal("Get() returned nil")
	}
	if got.Title != "Write code" {
		t.Errorf("Get().Title = %q, want %q", got.Title, "Write code")
	}
	if table.ParentID() != "parent-1" {
		t.Errorf("ParentID() = %q, want %q", table.ParentID(), "parent-1")
	}
}

func TestTaskProgressTable_UpdateStatus(t *testing.T) {
	table := NewTaskProgressTable("p1")
	table.Add(&TaskEntry{TaskID: "t1", Status: TaskDispatched, MaxRetries: 2})

	table.UpdateStatus("t1", TaskRunning)

	got := table.Get("t1")
	if got.Status != TaskRunning {
		t.Errorf("Status after UpdateStatus = %v, want %v", got.Status, TaskRunning)
	}
}

func TestTaskProgressTable_ListAll(t *testing.T) {
	table := NewTaskProgressTable("p1")
	table.Add(&TaskEntry{TaskID: "t3", Status: TaskDispatched})
	table.Add(&TaskEntry{TaskID: "t1", Status: TaskDispatched})
	table.Add(&TaskEntry{TaskID: "t2", Status: TaskDispatched})

	all := table.ListAll()
	if len(all) != 3 {
		t.Fatalf("ListAll() count = %d, want 3", len(all))
	}
	// Verify insertion order is preserved
	if all[0].TaskID != "t3" || all[1].TaskID != "t1" || all[2].TaskID != "t2" {
		t.Errorf("ListAll() order = %v, want [t3, t1, t2]", taskIDs(all))
	}
}

func taskIDs(entries []*TaskEntry) []string {
	ids := make([]string, len(entries))
	for i, e := range entries {
		ids[i] = e.TaskID
	}
	return ids
}

func TestTaskProgressTable_Counts(t *testing.T) {
	table := NewTaskProgressTable("p1")

	if table.Count() != 0 {
		t.Errorf("empty Count() = %d, want 0", table.Count())
	}

	table.Add(&TaskEntry{TaskID: "t1", Status: TaskSucceeded})
	table.Add(&TaskEntry{TaskID: "t2", Status: TaskFailed})
	table.Add(&TaskEntry{TaskID: "t3", Status: TaskRunning})

	if table.Count() != 3 {
		t.Errorf("Count() = %d, want 3", table.Count())
	}
	if table.CompletedCount() != 1 {
		t.Errorf("CompletedCount() = %d, want 1", table.CompletedCount())
	}
	if table.PendingCount() != 1 {
		t.Errorf("PendingCount() = %d, want 1", table.PendingCount()) // only t3 is non-terminal
	}
	if table.FailedCount() != 1 {
		t.Errorf("FailedCount() = %d, want 1", table.FailedCount())
	}
}

func TestTaskProgressTable_AllCompleted(t *testing.T) {
	table := NewTaskProgressTable("p1")
	table.Add(&TaskEntry{TaskID: "t1", Status: TaskRunning})

	if table.AllCompleted() {
		t.Error("AllCompleted() with running tasks should be false")
	}

	table.UpdateStatus("t1", TaskSucceeded)
	if !table.AllCompleted() {
		t.Error("AllCompleted() after all succeeded should be true")
	}
}

func TestTaskEntry_IsTerminal_IsCompletedSuccessfully_CanRetry(t *testing.T) {
	tests := []struct {
		status          TaskStatus
		isTerminal      bool
		isSuccess       bool
		canRetry        bool
	}{
		{TaskDispatched, false, false, false},
		{TaskAssigned, false, false, false},
		{TaskRunning, false, false, false},
		{TaskSucceeded, true, true, false},
		{TaskFailed, true, false, true},   // can retry if retries left
		{TaskTimedOut, true, false, false},
		{TaskCancelled, true, false, false},
	}

	for _, tt := range tests {
		e := &TaskEntry{Status: tt.status, RetryCount: 0, MaxRetries: 2}
		if got := e.IsTerminal(); got != tt.isTerminal {
			t.Errorf("IsTerminal(%v) = %v, want %v", tt.status, got, tt.isTerminal)
		}
		if got := e.IsCompletedSuccessfully(); got != tt.isSuccess {
			t.Errorf("IsCompletedSuccessfully(%v) = %v, want %v", tt.status, got, tt.isSuccess)
		}
		if got := e.CanRetry(); got != tt.canRetry {
			t.Errorf("CanRetry(%v) = %v, want %v", tt.status, got, tt.canRetry)
		}
	}
}

// ===========================================================================
// CoordState Tests
// ===========================================================================

func TestNewCoordState(t *testing.T) {
	cs := NewCoordState("parent-1", 30*time.Second, core.DefaultLogger())

	if cs.ParentTaskID != "parent-1" {
		t.Errorf("ParentTaskID = %q, want %q", cs.ParentTaskID, "parent-1")
	}
	if cs.LifecycleState != LifecycleRunning {
		t.Errorf("Initial LifecycleState = %v, want %v", cs.LifecycleState, LifecycleRunning)
	}
	if cs.TaskProgress == nil {
		t.Error("TaskProgress should not be nil")
	}
	if cs.ControlChan == nil {
		t.Error("ControlChan should not be nil")
	}
	if cs.GlobalTimer == nil {
		t.Error("GlobalTimer should not be nil for positive timeout")
	}

	cs.Dispose()
}

func TestNewCoordState_NoTimeout(t *testing.T) {
	cs := NewCoordState("parent-1", 0, core.DefaultLogger())

	if cs.GlobalTimer != nil {
		t.Error("GlobalTimer should be nil for zero timeout")
	}
	cs.Dispose()
}

func TestCoordState_Lifecycle_Transitions(t *testing.T) {
	cs := NewCoordState("parent-1", 0, core.DefaultLogger())
	defer cs.Dispose()

	// Running → Interrupted
	if err := cs.Interrupt("user request"); err != nil {
		t.Fatalf("Interrupt() error = %v", err)
	}
	if cs.LifecycleState != LifecycleInterrupted {
		t.Errorf("After Interrupt: State = %v, want %v", cs.LifecycleState, LifecycleInterrupted)
	}
	if cs.InterruptReason != "user request" {
		t.Errorf("InterruptReason = %q, want %q", cs.InterruptReason, "user request")
	}

	// Interrupted → Running (Resume)
	if err := cs.Resume(); err != nil {
		t.Fatalf("Resume() error = %v", err)
	}
	if cs.LifecycleState != LifecycleRunning {
		t.Errorf("After Resume: State = %v, want %v", cs.LifecycleState, LifecycleRunning)
	}
	if cs.InterruptReason != "" {
		t.Errorf("After Resume: InterruptReason should be empty, got %q", cs.InterruptReason)
	}

	// Running → Cancelled (terminal)
	if err := cs.Cancel("shutdown"); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	if cs.LifecycleState != LifecycleCancelled {
		t.Errorf("After Cancel: State = %v, want %v", cs.LifecycleState, LifecycleCancelled)
	}
	if cs.CancelReason != "shutdown" {
		t.Errorf("CancelReason = %q, want %q", cs.CancelReason, "shutdown")
	}

	// Terminal state — cannot transition further
	if err := cs.Resume(); err == nil {
		t.Error("Resume() on Cancelled state should error")
	}
}

func TestCoordState_InvalidTransitions(t *testing.T) {
	cs := NewCoordState("parent-1", 0, core.DefaultLogger())
	defer cs.Dispose()

	// Cannot interrupt when already completed
	cs.MarkCompleted()
	if err := cs.Interrupt("test"); err == nil {
		t.Error("Interrupt() on Completed should error")
	}

	// Cannot resume from running
	cs2 := NewCoordState("parent-2", 0, core.DefaultLogger())
	defer cs2.Dispose()
	if err := cs2.Resume(); err == nil {
		t.Error("Resume() from Running should error")
	}
}

func TestCoordState_RegisterUnregisterSubTask(t *testing.T) {
	cs := NewCoordState("parent-1", 0, core.DefaultLogger())
	defer cs.Dispose()

	taskCtx := cs.RegisterSubTask("task-1")
	if taskCtx == nil {
		t.Fatal("RegisterSubTask() returned nil context")
	}
	if _, ok := cs.SubTaskCtxs["task-1"]; !ok {
		t.Error("SubTaskCtxs should contain 'task-1'")
	}

	cs.UnregisterSubTask("task-1")
	if _, ok := cs.SubTaskCtxs["task-1"]; ok {
		t.Error("SubTaskCtxs should NOT contain 'task-1' after UnregisterSubTask")
	}
}
