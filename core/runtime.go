package core

import (
	"sync"
	"time"
)

// ===========================================================================
// Agent Runtime State — for orchestration layer
// ===========================================================================

// AgentState represents the current state of an agent in the runtime directory.
// Design §9.1 / §14.1 defines the full state machine:
//
//	idle → busy → idle (normal execution cycle)
//	idle → coordinating → idle (coordinator mode)
//	idle → dormant (idle timeout, design §12.4)
//	any → error (error recovery)
//	any → cancelled / interrupted (lifecycle control)
type AgentState string

const (
	// AgentStateIdle is the default state — agent is ready to accept tasks.
	AgentStateIdle AgentState = "idle"
	// AgentStateBusy means the agent is executing a task.
	AgentStateBusy AgentState = "busy"
	// AgentStateCoordinating means the agent is in Coordinator mode (managing sub-tasks).
	AgentStateCoordinating AgentState = "coordinating"
	// AgentStateDormant means the agent has been idle past the timeout threshold (design §12.4).
	// The agent may be evicted or reactivated on demand.
	AgentStateDormant AgentState = "dormant"
	// AgentStateError means the agent encountered an error and needs recovery.
	AgentStateError AgentState = "error"
)

// IsTerminal returns true if the state is a terminal/end state.
func (s AgentState) IsTerminal() bool {
	switch s {
	case AgentStateError:
		return true
	default:
		return false
	}
}

// CanAcceptTask returns true if the agent can accept a new task in this state.
func (s AgentState) CanAcceptTask() bool {
	switch s {
	case AgentStateIdle, AgentStateDormant:
		return true
	default:
		return false
	}
}

// ===========================================================================
// Agent Runtime Meta — lightweight runtime wrapper around AgentConfig
// ===========================================================================
//
// IMPORTANT: This type does NOT duplicate identity fields from AgentConfig.
// All static configuration (Name, Description, Role, Model, etc.) lives in
// AgentConfig — the single source of truth. AgentRuntimeMeta ONLY holds
// runtime-mutable state that changes during the agent's lifecycle.
//
// Construction: use NewAgentRuntimeMeta(config) to create from an AgentConfig.
// The Config field is read-only after construction; mutation only affects
// State/Score/TaskCount/LastActive.

// AgentRuntimeMeta holds lightweight runtime metadata for an orchestrator.
// Identity data is delegated to the embedded AgentConfig reference to avoid
// duplication. The Orchestrator uses this for routing decisions and state
// tracking, never loading Body to keep memory low.
type AgentRuntimeMeta struct {
	Config     *AgentConfig  // Immutable identity/configuration reference (required)
	State      AgentState    // Current runtime state (mutated by RuntimeDirectory)
	Score      float64       // Performance score (0-3 average)
	TaskCount  int64         // Total tasks completed
	LastActive time.Time     // Last activity timestamp
}

// NewAgentRuntimeMeta creates a runtime metadata entry from an AgentConfig.
// Panics if config is nil. CreatedAt defaults to time.Now().
func NewAgentRuntimeMeta(config *AgentConfig) *AgentRuntimeMeta {
	if config == nil {
		panic("goreact: NewAgentRuntimeMeta called with nil AgentConfig")
	}
	return &AgentRuntimeMeta{
		Config:     config,
		State:      AgentStateIdle,
		Score:      0,
		TaskCount:  0,
		LastActive: time.Now(),
	}
}

// ID returns the agent's unique identifier (delegates to Config.Name).
func (m *AgentRuntimeMeta) ID() string { return m.Config.Name }

// Name returns the agent's human-readable name (delegates to Config.Name).
func (m *AgentRuntimeMeta) Name() string { return m.Config.Name }

// Description returns the role description (delegates to Config.Description).
func (m *AgentRuntimeMeta) Description() string { return m.Config.Description }

// IsActive returns true if the agent is in an active non-error state.
func (m *AgentRuntimeMeta) IsActive() bool {
	return m.State != AgentStateError
}

// IsAvailable returns true if the agent can accept a new task.
func (m *AgentRuntimeMeta) IsAvailable() bool {
	return m.State == AgentStateIdle || m.State == AgentStateDormant
}

// ===========================================================================
// Runtime Directory — agent runtime state store
// ===========================================================================

// RuntimeDirectory manages agent runtime metadata (state, scores, task counts).
// Separate from file-based AgentRegistry which manages agent *definitions*.
type RuntimeDirectory struct {
	mu      sync.RWMutex
	agents  map[string]*AgentRuntimeMeta // key: agent ID
	maxSize int                          // 0 = unlimited
}

// NewRuntimeDirectory creates a new RuntimeDirectory.
func NewRuntimeDirectory(maxSize int) *RuntimeDirectory {
	if maxSize <= 0 {
		maxSize = 0
	}
	return &RuntimeDirectory{
		agents:  make(map[string]*AgentRuntimeMeta),
		maxSize: maxSize,
	}
}

// Register adds a new agent. ErrRuntimeDirDuplicate if ID exists.
func (d *RuntimeDirectory) Register(meta *AgentRuntimeMeta) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if _, exists := d.agents[meta.ID()]; exists {
		return ErrRuntimeDirDuplicate
	}
	if d.maxSize > 0 && len(d.agents) >= d.maxSize {
		return ErrRuntimeDirFull
	}
	d.agents[meta.ID()] = meta
	return nil
}

// Unregister removes an agent by ID.
func (d *RuntimeDirectory) Unregister(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.agents, id)
}

// Get retrieves a copy of agent metadata by ID. Nil if not found.
func (d *RuntimeDirectory) Get(id string) *AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if meta, ok := d.agents[id]; ok {
		cp := *meta
		return &cp
	}
	return nil
}

// SetState updates the state of a registered agent. No-op if not found.
func (d *RuntimeDirectory) SetState(id string, state AgentState) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if meta, ok := d.agents[id]; ok {
		meta.State = state
		meta.LastActive = time.Now()
	}
}

// SetScore updates the score of a registered agent.
func (d *RuntimeDirectory) SetScore(id string, score float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if meta, ok := d.agents[id]; ok {
		meta.Score = score
	}
}

// ListAll returns copies of all registered agents.
func (d *RuntimeDirectory) ListAll() []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	result := make([]*AgentRuntimeMeta, 0, len(d.agents))
	for _, meta := range d.agents {
		cp := *meta
		result = append(result, &cp)
	}
	return result
}

// ListAvailable returns idle agents sorted by score descending.
func (d *RuntimeDirectory) ListAvailable() []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []*AgentRuntimeMeta
	for _, meta := range d.agents {
		if meta.IsAvailable() {
			cp := *meta
			result = append(result, &cp)
		}
	}
	sortByScore(result)
	return result
}

// ListActive returns non-error agents sorted by score descending.
func (d *RuntimeDirectory) ListActive() []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []*AgentRuntimeMeta
	for _, meta := range d.agents {
		if meta.IsActive() {
			cp := *meta
			result = append(result, &cp)
		}
	}
	sortByScore(result)
	return result
}

// FindByDescription searches agents whose description contains query (case-insensitive).
func (d *RuntimeDirectory) FindByDescription(query string) []*AgentRuntimeMeta {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var result []*AgentRuntimeMeta
	for _, meta := range d.agents {
		if meta.IsActive() && containsIgnoreCase(meta.Description(), query) {
			cp := *meta
			result = append(result, &cp)
		}
	}
	return result
}

// Count returns total number of registered agents.
func (d *RuntimeDirectory) Count() int {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return len(d.agents)
}

// IncrementTaskCount bumps the task counter and updates LastActive.
func (d *RuntimeDirectory) IncrementTaskCount(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if meta, ok := d.agents[id]; ok {
		meta.TaskCount++
		meta.LastActive = time.Now()
	}
}

// ===========================================================================
// Errors
// ===========================================================================

var (
	ErrRuntimeDirDuplicate = newRuntimeErr("agent already registered")
	ErrRuntimeDirFull     = newRuntimeErr("runtime directory full")
	ErrRuntimeDirNotFound = newRuntimeErr("agent not found")
)

type runtimeErr struct{ msg string }

func newRuntimeErr(msg string) error { return &runtimeErr{msg} }
func (e *runtimeErr) Error() string        { return "runtime directory: " + e.msg }

// ===========================================================================
// Internal helpers
// ===========================================================================

func sortByScore(agents []*AgentRuntimeMeta) {
	for i := 1; i < len(agents); i++ {
		key := agents[i]
		j := i - 1
		for ; j >= 0 && agents[j].Score < key.Score; j-- {
			agents[j+1] = agents[j]
		}
		agents[j+1] = key
	}
}

func containsIgnoreCase(s, substr string) bool {
	if len(s) == 0 || len(substr) == 0 {
		return false
	}
	sLower := make([]byte, len(s))
	subLower := make([]byte, len(substr))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		sLower[i] = c
	}
	for i := 0; i < len(substr); i++ {
		c := substr[i]
		if c >= 'A' && c <= 'Z' {
			c += 32
		}
		subLower[i] = c
	}
	for i := 0; i <= len(sLower)-len(subLower); i++ {
		if string(sLower[i:i+len(subLower)]) == string(subLower) {
			return true
		}
	}
	return false
}

// ===========================================================================
// Control Command — lifecycle control for Coordinator mode
// ===========================================================================

// ControlCommand represents a lifecycle control instruction for a Coordinator.
// Design §10.5.7 defines the wire format with Requester for priority resolution (§10.5.4).
type ControlCommand struct {
	Action    string    // "interrupt" | "resume" | "cancel"
	Reason    string    // Human-readable reason for the command
	Requester string    // "user" | "system" | "parent_coordinator" — source (§10.5.4)
	Timestamp time.Time // When the command was issued
	Deadline  time.Time // Optional deadline for resume
}

// Control command constants.
const (
	CmdInterrupt = "interrupt"
	CmdResume    = "resume"
	CmdCancel    = "cancel"
)

// ControlRequester constants define who issued a lifecycle control command.
const (
	RequesterUser              = "user"
	RequesterSystem            = "system"
	RequesterParentCoordinator = "parent_coordinator"
)

// Priority returns numeric priority: User(3) > ParentCoordinator(2) > System(1).
func (c *ControlCommand) Priority() int {
	switch c.Requester {
	case RequesterUser:
		return 3
	case RequesterParentCoordinator:
		return 2
	case RequesterSystem:
		return 1
	default:
		return 0
	}
}

// ===========================================================================
// Task Result Event — execution result from agent back through Orchestrator
// ===========================================================================

// TaskResultEvent carries execution results from an agent.
type TaskResultEvent struct {
	TaskID        string
	TargetAgentID string
	Result        string
	Error         error
	Duration      time.Duration
	Timestamp     time.Time
}

// TimeoutWarningEvent is sent when a task approaches its timeout limit.
type TimeoutWarningEvent struct {
	TaskID    string
	Elapsed   time.Duration
	Remaining time.Duration
	Timestamp time.Time
}

// TimeoutEvent is sent when a task has exceeded its timeout.
type TimeoutEvent struct {
	TaskID    string
	Elapsed   time.Duration
	Timestamp time.Time
}

// ===========================================================================
// Additional Event Types — design §7.1 + §10.5.7
// ===========================================================================

// AgentScoreEvent is emitted by a Coordinator to report an agent's quality score
// back to the Orchestrator for performance tracking (design §7.1).
type AgentScoreEvent struct {
	AgentID  string // Agent being scored
	TaskID   string // Task that was scored
	Score    int    // 0-3 (ScoreFailed to ScorePerfect)
	Reason   string // Human-readable scoring rationale
	Timestamp time.Time
}

// TaskAssignedEvent is emitted by the Orchestrator to notify a Coordinator (or agent)
// that a task has been assigned to a specific executor (design §7.1).
type TaskAssignedEvent struct {
	TaskID        string
	TargetAgentID string // Agent that will execute the task
	Priority      int
	Timestamp     time.Time
}

// CoordLifecycleEvent is emitted by a Coordinator to notify the Orchestrator of
// its own lifecycle state transitions (design §10.5.7).
type CoordLifecycleEvent struct {
	CoordinatorID  string
	OldState       string // Previous lifecycle state
	NewState       string // Current lifecycle state
	Reason         string
	Timestamp      time.Time
}


