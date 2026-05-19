package reactor

import (
	"fmt"
	"sync"
	"time"
)

// ===========================================================================
// Agent Mode — Executor
// ===========================================================================

type AgentMode string

const (
	ModeExecutor AgentMode = "executor"
)

func (m AgentMode) String() string {
	return string(m)
}

func (m AgentMode) IsExecutor() bool { return m == ModeExecutor }


// ===========================================================================
// Lifecycle State — Coordinator lifecycle management
// ===========================================================================

type LifecycleState string

const (
	LifecycleRunning    LifecycleState = "running"
	LifecycleInterrupted LifecycleState = "interrupted"
	LifecycleCancelled  LifecycleState = "cancelled"
	LifecycleCompleted  LifecycleState = "completed"
)

func (s LifecycleState) IsTerminal() bool {
	return s == LifecycleCancelled || s == LifecycleCompleted
}

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

type TaskStatus string

const (
	TaskDispatched   TaskStatus = "dispatched"
	TaskAssigned     TaskStatus = "assigned"
	TaskRunning      TaskStatus = "running"
	TaskSucceeded    TaskStatus = "succeeded"
	TaskFailed       TaskStatus = "failed"
	TaskTimedOut     TaskStatus = "timed_out"
	TaskCancelled    TaskStatus = "cancelled"
	TaskRetryPending TaskStatus = "retry_pending"
)

type TaskEntry struct {
	TaskID       string
	Title        string
	Description  string
	Priority     int
	Status       TaskStatus
	Result       *TaskResultHolder
	Error        error
	RetryCount   int
	MaxRetries   int
	DispatchedAt *time.Time
	StartedAt    *time.Time
	CompletedAt  *time.Time
	PausedAt     *time.Time
	Duration     time.Duration
}

type TaskResultHolder struct {
	Content  string
	AgentID  string
	Duration time.Duration
	Score    int
}

func (e *TaskEntry) IsTerminal() bool {
	switch e.Status {
	case TaskSucceeded, TaskFailed, TaskTimedOut, TaskCancelled:
		return true
	default:
		return false
	}
}

func (e *TaskEntry) IsCompletedSuccessfully() bool { return e.Status == TaskSucceeded }

func (e *TaskEntry) CanRetry() bool {
	return e.Status == TaskFailed && e.RetryCount < e.MaxRetries
}

type TaskProgressTable struct {
	mu       sync.RWMutex
	entries  map[string]*TaskEntry
	parentID string
	order    []string
}

func NewTaskProgressTable(parentTaskID string) *TaskProgressTable {
	return &TaskProgressTable{
		entries:  make(map[string]*TaskEntry),
		parentID: parentTaskID,
		order:    make([]string, 0),
	}
}

func (t *TaskProgressTable) Add(entry *TaskEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, exists := t.entries[entry.TaskID]; !exists {
		t.order = append(t.order, entry.TaskID)
	}
	t.entries[entry.TaskID] = entry
}

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

type TaskUpdateOption func(*TaskEntry, time.Time)

func WithResult(result *TaskResultHolder) TaskUpdateOption {
	return func(e *TaskEntry, _ time.Time) { e.Result = result }
}

func WithError(err error) TaskUpdateOption {
	return func(e *TaskEntry, _ time.Time) { e.Error = err }
}

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

func WithIncrementRetry() TaskUpdateOption {
	return func(e *TaskEntry, _ time.Time) { e.RetryCount++ }
}

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

func (t *TaskProgressTable) Count() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.entries)
}

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

func (t *TaskProgressTable) ParentID() string { return t.parentID }

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
