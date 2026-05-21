package reactor

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/DotNetAge/goreact/core"
)

// ===========================================================================
// CoordState — Coordinator runtime state (attached to ReactContext)
// ===========================================================================

// CoordState holds the runtime state of a Coordinator, attached to ReactContext.
// It manages lifecycle state transitions, sub-task contexts/cancellation,
// task progress tracking, and control command handling.
//
// Fields:
//   - ParentTaskID:    The ID of the parent task that spawned this coordinator.
//   - TaskProgress:    Tracks the progress of all sub-tasks dispatched by this coordinator.
//   - SubTaskResults:  Maps sub-task IDs to their final results (populated on completion).
//   - DispatchedAt:    Timestamp when this coordination session was created.
//   - GlobalTimer:     Optional timer for overall execution timeout; fires a cancel command on expiry.
//   - Logger:          Structured logger for lifecycle and diagnostic messages.
//
// Protected fields (require mu lock):
//   - LifecycleCtx:    Context tied to the coordinator's lifecycle (cancelled on Cancel/Dispose).
//   - LifecycleCancel: Cancellation function for LifecycleCtx.
//   - SubTaskCtxs:     Per-sub-task derived contexts (child of LifecycleCtx).
//   - SubTaskCancels:  Per-sub-task cancellation functions.
//   - LifecycleState:  Current state in the lifecycle FSM (Running/Interrupted/Cancelled/Completed).
//   - ControlChan:     Buffered channel for receiving external control commands (cancel, pause, etc.).
//   - InterruptReason: Human-readable reason for the last interrupt.
//   - InterruptedAt:   Timestamp of the last interrupt.
//   - CancelReason:    Human-readable reason for cancellation.
type CoordState struct {
	ParentTaskID    string
	TaskProgress    *TaskProgressTable
	SubTaskResults  map[string]*core.TaskResultEvent
	DispatchedAt    time.Time
	GlobalTimer     *time.Timer
	Logger          core.Logger

	mu              sync.Mutex
	LifecycleCtx    context.Context
	LifecycleCancel context.CancelFunc
	SubTaskCtxs     map[string]context.Context
	SubTaskCancels  map[string]context.CancelFunc
	LifecycleState  LifecycleState
	ControlChan     chan *core.ControlCommand
	InterruptReason string
	InterruptedAt   time.Time
	CancelReason    string
}

// NewCoordState creates and initializes a new CoordState for the given parent task.
//
// Parameters:
//   - parentTaskID:    The ID of the parent task that owns this coordination session.
//   - overallTimeout:  If > 0, a global timer is started that auto-cancels the coordinator on expiry.
//   - logger:         Structured logger for lifecycle event reporting.
//
// The returned CoordState starts in LifecycleRunning state with an empty sub-task registry.
func NewCoordState(parentTaskID string, overallTimeout time.Duration, logger core.Logger) *CoordState {
	ctx, cancel := context.WithCancel(context.Background())
	cs := &CoordState{
		ParentTaskID:    parentTaskID,
		TaskProgress:    NewTaskProgressTable(parentTaskID),
		SubTaskResults:  make(map[string]*core.TaskResultEvent),
		DispatchedAt:    time.Now(),
		LifecycleCtx:    ctx,
		LifecycleCancel: cancel,
		SubTaskCtxs:     make(map[string]context.Context),
		SubTaskCancels:  make(map[string]context.CancelFunc),
		LifecycleState:  LifecycleRunning,
		ControlChan:     make(chan *core.ControlCommand, 8),
		Logger:          logger,
	}

	if overallTimeout > 0 {
		cs.GlobalTimer = time.AfterFunc(overallTimeout, func() {
			select {
			case cs.ControlChan <- &core.ControlCommand{Action: core.CmdCancel, Reason: "global timeout exceeded", Timestamp: time.Now()}:
			default:
			}
		})
	}

	return cs
}

// Dispose releases all resources held by the CoordState.
// It cancels the lifecycle context, stops the global timer, closes the control channel,
// and cancels all remaining sub-task contexts. Safe to call multiple times.
// After Dispose, the CoordState should not be used for new operations.
func (cs *CoordState) Dispose() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.LifecycleCancel != nil {
		cs.LifecycleCancel()
	}
	if cs.GlobalTimer != nil {
		cs.GlobalTimer.Stop()
	}

	if cs.ControlChan != nil {
		select {
		case <-cs.ControlChan:
		default:
			close(cs.ControlChan)
		}
		cs.ControlChan = nil
	}

	for _, cancel := range cs.SubTaskCancels {
		if cancel != nil {
			cancel()
		}
	}
}

// ===========================================================================
// Coordinator Lifecycle Control Methods
// ===========================================================================

// Interrupt transitions the coordinator from Running to Interrupted state.
// All active sub-tasks are cancelled with reason "coordinator interrupted".
//
// Parameters:
//   - reason: Human-readable explanation for the interrupt (e.g., "user requested pause").
//
// Returns an error if the coordinator is not in the Running state.
func (cs *CoordState) Interrupt(reason string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.checkLifecycleTransition(LifecycleInterrupted, reason)

	if cs.LifecycleState != LifecycleRunning {
		return fmt.Errorf("coordinator: cannot interrupt from state %s", cs.LifecycleState)
	}

	cs.cancelAllSubTasks()

	cs.LifecycleState = LifecycleInterrupted
	cs.InterruptReason = reason
	cs.InterruptedAt = time.Now()

	return nil
}

// Resume transitions the coordinator from Interrupted back to Running state.
// A fresh lifecycle context is created and non-terminal sub-task contexts are recreated.
//
// Returns an error if the coordinator is not in the Interrupted state.
func (cs *CoordState) Resume() error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	cs.checkLifecycleTransition(LifecycleRunning, "")

	if cs.LifecycleState != LifecycleInterrupted {
		return fmt.Errorf("coordinator: cannot resume from state %s", cs.LifecycleState)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cs.LifecycleCtx = ctx
	cs.LifecycleCancel = cancel

	cs.recreateInterruptedSubTaskContexts()

	cs.LifecycleState = LifecycleRunning
	cs.InterruptReason = ""
	cs.InterruptedAt = time.Time{}

	return nil
}

// Cancel transitions the coordinator to the Cancelled terminal state.
// It cancels the lifecycle context and all sub-task contexts.
//
// Parameters:
//   - reason: Human-readable explanation for the cancellation.
//
// Returns an error if the coordinator is already in a terminal state (Cancelled or Completed).
func (cs *CoordState) Cancel(reason string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	targetState := LifecycleCancelled
	cs.checkLifecycleTransition(targetState, reason)

	if cs.LifecycleState.IsTerminal() {
		return fmt.Errorf("coordinator: cannot cancel from terminal state %s", cs.LifecycleState)
	}

	if cs.LifecycleCancel != nil {
		cs.LifecycleCancel()
	}
	cs.cancelAllSubTasks()

	cs.LifecycleState = targetState
	cs.CancelReason = reason

	return nil
}

// MarkCompleted transitions the coordinator to the Completed terminal state.
// No-op if already in a terminal state. This is typically called when all
// sub-tasks have finished successfully or the coordinator's work is done.
func (cs *CoordState) MarkCompleted() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.LifecycleState.IsTerminal() {
		return
	}
	cs.LifecycleState = LifecycleCompleted
}

// RegisterSubTask creates a new derived context for a sub-task, child of the coordinator's
// lifecycle context. The sub-task will be automatically cancelled when the coordinator's
// lifecycle context is cancelled or when Interrupt/Cancel is called.
//
// Parameters:
//   - taskID: Unique identifier for the sub-task.
//
// Returns the sub-task-specific context that should be used for the sub-task's operations.
// RegisterSubTask creates a new sub-task context derived from the coordinator's lifecycle context.
// The sub-task will be automatically cancelled when the coordinator is cancelled or interrupted.
//
// Parameters:
//   - taskID: Unique identifier for the sub-task (used as key in the registry).
//
// Returns a context.Context that should be used for the sub-task's operations.
//
// Thread Safety: This method acquires cs.mu lock to protect SubTaskCtxs and SubTaskCancels maps.
func (cs *CoordState) RegisterSubTask(taskID string) context.Context {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	taskCtx, taskCancel := context.WithCancel(cs.LifecycleCtx)
	cs.SubTaskCtxs[taskID] = taskCtx
	cs.SubTaskCancels[taskID] = taskCancel
	return taskCtx
}

// UnregisterSubTask removes a sub-task's context and cancel function from the registry.
// This should be called when a sub-task completes (successfully or not) to clean up resources.
// Note: this does NOT cancel the sub-task context; call the cancel function separately if needed.
//
// Thread Safety: This method acquires cs.mu lock to protect SubTaskCtxs and SubTaskCancels maps.
func (cs *CoordState) UnregisterSubTask(taskID string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	delete(cs.SubTaskCtxs, taskID)
	delete(cs.SubTaskCancels, taskID)
}

// ===========================================================================
// Internal helpers
// ===========================================================================

// checkLifecycleTransition validates and logs a lifecycle state transition.
// It warns on invalid transitions but does not block them (the caller is responsible for that).
// Valid transitions are enforced by the LifecycleState.CanTransitionTo method.
func (cs *CoordState) checkLifecycleTransition(target LifecycleState, reason string) {
	from := cs.LifecycleState
	if from == target {
		return
	}
	if !from.CanTransitionTo(target) {
		cs.Logger.Warn("invalid lifecycle state transition",
			"from", from,
			"to", target,
			"reason", reason,
		)
		return
	}
	cs.Logger.Info("coordinator lifecycle transition",
		"from", from,
		"to", target,
		"reason", reason,
	)
}

// cancelAllSubTasks cancels every registered sub-task context and marks their
// progress entries as TaskCancelled with an appropriate error message.
// Must be called while holding cs.mu.
func (cs *CoordState) cancelAllSubTasks() {
	for taskID, cancel := range cs.SubTaskCancels {
		if cancel != nil {
			cancel()
		}
		if cs.TaskProgress != nil {
			cs.TaskProgress.UpdateStatus(taskID, TaskCancelled, WithError(errors.New("coordinator interrupted")))
		}
	}
}

// recreateInterruptedSubTaskContexts rebuilds sub-task contexts for all non-terminal
// tasks that were in progress (not Dispatched or Assigned) when the interrupt occurred.
// This is called during Resume to allow interrupted tasks to continue execution
// under a fresh lifecycle context. Tasks that were still waiting (Dispatched/Assigned)
// are left as-is since they haven't started yet.
func (cs *CoordState) recreateInterruptedSubTaskContexts() {
	if cs.TaskProgress == nil {
		return
	}

	for _, entry := range cs.TaskProgress.ListAll() {
		if !entry.IsTerminal() && entry.Status != TaskDispatched && entry.Status != TaskAssigned {
			taskCtx, taskCancel := context.WithCancel(cs.LifecycleCtx)
			cs.SubTaskCtxs[entry.TaskID] = taskCtx
			cs.SubTaskCancels[entry.TaskID] = taskCancel
			cs.TaskProgress.UpdateStatus(entry.TaskID, TaskDispatched)
		}
	}
}

// ErrInvalidLifecycleTransition is returned when an attempted lifecycle state
// transition is not permitted by the state machine rules.
var ErrInvalidLifecycleTransition = errors.New("invalid lifecycle state transition")
