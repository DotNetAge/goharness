package reactor

import (
	"fmt"
	"sync"
	"time"
)

// ===========================================================================
// Agent Mode — Executor
// ===========================================================================

// AgentMode defines the execution mode of an agent within the coordinator.
// Currently only "executor" mode is supported, where the agent directly executes tasks.
type AgentMode string

const (
	// ModeExecutor indicates the agent executes tasks directly (default mode).
	ModeExecutor AgentMode = "executor"
)

// String returns the string representation of the AgentMode.
func (m AgentMode) String() string { return string(m) }

// IsExecutor returns true if the mode is ModeExecutor.
func (m AgentMode) IsExecutor() bool { return m == ModeExecutor }

// ===========================================================================
// Lifecycle State — Coordinator lifecycle management
// ===========================================================================

// LifecycleState represents the current state in the coordinator's lifecycle finite state machine.
//
// Valid state transitions:
//   - Running  → Interrupted, Cancelled, Completed
//   - Interrupted → Running, Cancelled
//   - Cancelled, Completed → (terminal, no further transitions)
type LifecycleState string

const (
	// LifecycleRunning is the initial/active state where the coordinator is processing tasks.
	LifecycleRunning LifecycleState = "running"
	// LifecycleInterrupted indicates the coordinator was paused (can resume to Running).
	LifecycleInterrupted LifecycleState = "interrupted"
	// LifecycleCancelled is a terminal state indicating the coordinator was cancelled.
	LifecycleCancelled LifecycleState = "cancelled"
	// LifecycleCompleted is a terminal state indicating all work finished successfully.
	LifecycleCompleted LifecycleState = "completed"
)

// IsTerminal returns true if the state is a terminal state (Cancelled or Completed),
// from which no further transitions are possible.
func (s LifecycleState) IsTerminal() bool {
	return s == LifecycleCancelled || s == LifecycleCompleted
}

// CanTransitionTo checks whether a transition from the receiver state to the target
// state is valid according to the lifecycle FSM rules.
func (from LifecycleState) CanTransitionTo(to LifecycleState) bool {
	switch from {
	case LifecycleRunning:
		return to == LifecycleInterrupted || to == LifecycleCancelled || to == LifecycleCompleted
	case LifecycleInterrupted:
		return to == LifecycleRunning || to == LifecycleCancelled
	default:
		return false
	}
}

// ===========================================================================
// Task Progress Table — Coordinator's core data structure
// ===========================================================================

// TaskStatus represents the execution status of a single sub-task within a coordination session.
type TaskStatus string

const (
	// TaskDispatched means the task has been created and queued but not yet assigned to an agent.
	TaskDispatched TaskStatus = "dispatched"
	// TaskAssigned means the task has been assigned to an agent but execution hasn't started.
	TaskAssigned TaskStatus = "assigned"
	// TaskRunning means the task is currently being executed by an agent.
	TaskRunning TaskStatus = "running"
	// TaskSucceeded means the task completed successfully (terminal state).
	TaskSucceeded TaskStatus = "succeeded"
	// TaskFailed means the task failed with an error (terminal state, may be retryable).
	TaskFailed TaskStatus = "failed"
	// TaskTimedOut means the task exceeded its execution time limit (terminal state).
	TaskTimedOut TaskStatus = "timed_out"
	// TaskCancelled means the task was cancelled before completion (terminal state).
	TaskCancelled TaskStatus = "cancelled"
	// TaskRetryPending means the task failed and is waiting to be retried.
	TaskRetryPending TaskStatus = "retry_pending"
)

// TaskEntry represents a single task's full state within the TaskProgressTable.
// It tracks the task's identity, status, timing, result, and retry information.
type TaskEntry struct {
	TaskID       string        // Unique identifier for this task.
	Title        string        // Human-readable title of the task.
	Description  string        // Detailed description of what the task should accomplish.
	Priority     int           // Execution priority (higher = more important).
	Status       TaskStatus    // Current execution status.
	Result       *TaskResultHolder // Final result, populated on success or failure.
	Error        error         // Error that caused failure, if any.
	RetryCount   int           // Number of retry attempts so far.
	MaxRetries   int           // Maximum allowed retries (0 = no retry).
	DispatchedAt *time.Time    // When the task was dispatched to the coordinator.
	StartedAt    *time.Time    // When the agent started executing this task.
	CompletedAt  *time.Time    // When the task reached a terminal state.
	PausedAt     *time.Time    // When the task was last paused (for interrupt/resume).
	Duration     time.Duration // Total wall-clock execution time (StartedAt → CompletedAt).
}

// TaskResultHolder wraps the output of a completed task with metadata.
type TaskResultHolder struct {
	Content  string        // The actual result content/output from the agent.
	AgentID  string        // ID of the agent that produced this result.
	Duration time.Duration // How long the agent took to produce this result.
	Score    int           // Optional quality/confidence score (0 = unset).
}

// IsTerminal returns true if the task is in a terminal state (Succeeded, Failed, TimedOut, or Cancelled),
// meaning no further state transitions are possible.
func (e *TaskEntry) IsTerminal() bool {
	switch e.Status {
	case TaskSucceeded, TaskFailed, TaskTimedOut, TaskCancelled:
		return true
	default:
		return false
	}
}

// IsCompletedSuccessfully returns true if the task completed without error (Status == TaskSucceeded).
func (e *TaskEntry) IsCompletedSuccessfully() bool { return e.Status == TaskSucceeded }

// CanRetry returns true if the task failed and has not exhausted its retry budget
// (RetryCount < MaxRetries and Status == TaskFailed).
func (e *TaskEntry) CanRetry() bool {
	return e.Status == TaskFailed && e.RetryCount < e.MaxRetries
}

// TaskProgressTable is a thread-safe registry of all sub-tasks within a coordination session.
// It maintains insertion order (via the order slice) and provides queries for status-based
// filtering, counting, and completion detection. All reads return shallow copies to prevent
// external mutation of internal state.
type TaskProgressTable struct {
	mu       sync.RWMutex
	entries  map[string]*TaskEntry
	parentID string
	order    []string
}

// NewTaskProgressTable creates an empty TaskProgressTable owned by the given parent task.
func NewTaskProgressTable(parentTaskID string) *TaskProgressTable {
	return &TaskProgressTable{
		entries:  make(map[string]*TaskEntry),
		parentID: parentTaskID,
		order:    make([]string, 0),
	}
}

// Add inserts or updates a task entry in the table. If the TaskID is new, it is appended
// to the insertion order. Existing entries are overwritten in place.
func (t *TaskProgressTable) Add(entry *TaskEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, exists := t.entries[entry.TaskID]; !exists {
		t.order = append(t.order, entry.TaskID)
	}
	t.entries[entry.TaskID] = entry
}

// Get retrieves a shallow copy of the task entry for the given taskID.
// Returns nil if the task is not found.
func (t *TaskProgressTable) Get(taskID string) *TaskEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()

	e, ok := t.entries[taskID]
	if !ok {
		return nil
	}
	cp := *e
	return &cp
}

// UpdateStatus updates the status of the task identified by taskID and applies
// any additional options (e.g., setting result, error, timestamps). No-op if taskID not found.
func (t *TaskProgressTable) UpdateStatus(taskID string, status TaskStatus, opts ...TaskUpdateOption) {
	t.mu.Lock()
	defer t.mu.Unlock()

	e, ok := t.entries[taskID]
	if !ok {
		return
	}

	e.Status = status
	now := time.Now()
	for _, opt := range opts {
		opt(e, now)
	}
}

// TaskUpdateOption is a functional option for modifying a TaskEntry during status updates.
type TaskUpdateOption func(*TaskEntry, time.Time)

// WithResult sets the Result field on the task entry.
func WithResult(result *TaskResultHolder) TaskUpdateOption {
	return func(e *TaskEntry, _ time.Time) { e.Result = result }
}

// WithError sets the Error field on the task entry.
func WithError(err error) TaskUpdateOption {
	return func(e *TaskEntry, _ time.Time) { e.Error = err }
}

// WithTimestamps automatically sets StartedAt when transitioning to TaskRunning,
// and sets CompletedAt + Duration when transitioning to any terminal state.
func WithTimestamps() TaskUpdateOption {
	return func(e *TaskEntry, now time.Time) {
		switch e.Status {
		case TaskRunning:
			e.StartedAt = &now
		case TaskSucceeded, TaskFailed, TaskTimedOut, TaskCancelled:
			e.CompletedAt = &now
			if e.StartedAt != nil {
				e.Duration = now.Sub(*e.StartedAt)
			}
		}
	}
}

// WithIncrementRetry increments the RetryCount field by 1.
func WithIncrementRetry() TaskUpdateOption {
	return func(e *TaskEntry, _ time.Time) { e.RetryCount++ }
}

// ListAll returns shallow copies of all task entries in insertion order.
func (t *TaskProgressTable) ListAll() []*TaskEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([]*TaskEntry, 0, len(t.order))
	for _, id := range t.order {
		if e, ok := t.entries[id]; ok {
			cp := *e
			result = append(result, &cp)
		}
	}
	return result
}

// ListByStatus returns shallow copies of all task entries matching the given status, in insertion order.
func (t *TaskProgressTable) ListByStatus(status TaskStatus) []*TaskEntry {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var result []*TaskEntry
	for _, id := range t.order {
		if e, ok := t.entries[id]; ok && e.Status == status {
			cp := *e
			result = append(result, &cp)
		}
	}
	return result
}

// Count returns the total number of tasks in the table.
func (t *TaskProgressTable) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.entries)
}

// PendingCount returns the number of tasks that are not yet in a terminal state.
func (t *TaskProgressTable) PendingCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	count := 0
	for _, id := range t.order {
		if e, ok := t.entries[id]; ok && !e.IsTerminal() {
			count++
		}
	}
	return count
}

// CompletedCount returns the number of tasks that succeeded (Status == TaskSucceeded).
func (t *TaskProgressTable) CompletedCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	count := 0
	for _, id := range t.order {
		if e, ok := t.entries[id]; ok && e.IsCompletedSuccessfully() {
			count++
		}
	}
	return count
}

// FailedCount returns the number of tasks that failed, timed out, or were cancelled.
func (t *TaskProgressTable) FailedCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	count := 0
	for _, id := range t.order {
		if e, ok := t.entries[id]; ok {
			switch e.Status {
			case TaskFailed, TaskTimedOut, TaskCancelled:
				count++
			}
		}
	}
	return count
}

// AllCompleted returns true if every task in the table is in a terminal state.
func (t *TaskProgressTable) AllCompleted() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	for _, id := range t.order {
		if e, ok := t.entries[id]; ok && !e.IsTerminal() {
			return false
		}
	}
	return true
}

// ParentID returns the parent task ID that owns this progress table.
func (t *TaskProgressTable) ParentID() string { return t.parentID }

// Summary returns a human-readable one-line summary of the table's state,
// including total, succeeded, failed, and pending counts.
func (t *TaskProgressTable) Summary() string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	total := len(t.entries)
	succeeded := 0
	failed := 0
	pending := 0

	for _, id := range t.order {
		if e, ok := t.entries[id]; ok {
			switch {
			case e.IsCompletedSuccessfully():
				succeeded++
			case e.IsTerminal():
				failed++
			default:
				pending++
			}
		}
	}

	return fmt.Sprintf("ProgressTable[parent=%s]: total=%d succeeded=%d failed=%d pending=%d",
		t.parentID, total, succeeded, failed, pending)
}
