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

func (cs *CoordState) MarkCompleted() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	if cs.LifecycleState.IsTerminal() {
		return
	}
	cs.LifecycleState = LifecycleCompleted
}

func (cs *CoordState) RegisterSubTask(taskID string) context.Context {
	taskCtx, taskCancel := context.WithCancel(cs.LifecycleCtx)
	cs.SubTaskCtxs[taskID] = taskCtx
	cs.SubTaskCancels[taskID] = taskCancel
	return taskCtx
}

func (cs *CoordState) UnregisterSubTask(taskID string) {
	delete(cs.SubTaskCtxs, taskID)
	delete(cs.SubTaskCancels, taskID)
}

// ===========================================================================
// Internal helpers
// ===========================================================================

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

var ErrInvalidLifecycleTransition = errors.New("invalid lifecycle state transition")
