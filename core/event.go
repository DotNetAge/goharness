package core

import "time"

// ReactEventType identifies the type of agent-level event.
// These are distinct from gochat/core.StreamEventType which operates at the LLM token level.
// ReactEvent operates at the agent business logic level (T-A-O cycles, tool calls, subtasks).
type ReactEventType string

const (
	// ThinkingDelta is a fragment of the Think phase output (streaming).
	ThinkingDelta ReactEventType = "thinking_delta"

	// ThinkingDone signals the Think phase has completed and the full thought is available.
	ThinkingDone ReactEventType = "thinking_done"

	// ActionStart signals the Act phase has begun with the given tools.
	ActionStart ReactEventType = "action_start"

	// ActionProgress reports progress of the current Act phase (e.g., "3/5 tools complete").
	ActionProgress ReactEventType = "action_progress"

	// ToolExecStart signals a specific tool is about to start executing.
	ToolExecStart ReactEventType = "tool_exec_start"

	// ToolExecEnd signals a specific tool has completed execution.
	ToolExecEnd ReactEventType = "tool_exec_end"

	// ActionEnd signals all tools in the current Act phase have completed.
	ActionEnd ReactEventType = "action_end"

	// ObservationDone signals the Observe phase has completed.
	ObservationDone ReactEventType = "observation_done"

	// SubtaskSpawned signals a subagent task has been created.
	SubtaskSpawned ReactEventType = "subtask_spawned"

	// SubtaskCompleted signals a subagent task has finished (success or failure).
	SubtaskCompleted ReactEventType = "subtask_completed"

	// FinalAnswer signals the Reactor has produced its final answer.
	FinalAnswer ReactEventType = "final_answer"

	// ClarifyNeeded signals the Reactor needs user clarification.
	ClarifyNeeded ReactEventType = "clarify_needed"

	// PermissionRequest signals a tool needs user authorization before execution.
	PermissionRequest ReactEventType = "permission_request"

	// PermissionDenied signals a tool execution was denied by the permission system.
	PermissionDenied ReactEventType = "permission_denied"

	// ExecutionSummary signals the reactor has completed and provides usage statistics.
	ExecutionSummary ReactEventType = "execution_summary"

	// Error signals an error at the reactor level.
	Error ReactEventType = "error"

	// CycleEnd signals one complete T-A-O cycle has ended.
	CycleEnd ReactEventType = "cycle_end"

	// TaskSummary signals a natural-language summary of the completed task.
	// This is emitted after the T-A-O loop finishes for non-trivial tasks.
	TaskSummary ReactEventType = "task_summary"
)

// ReactEvent is the unit of data published by the Reactor's event bus.
// Each event carries a TaskID so subscribers can route events to the correct UI panel.
type ReactEvent struct {
	// SessionID identifies the conversation session.
	SessionID string `json:"session_id"`

	// AgentID identifies the source agent: "main" for the primary agent,
	// or the subagent name for delegated tasks.
	AgentID string `json:"agent_id,omitempty"`

	// TaskID identifies the source task: "main" for the primary reactor,
	// "task_1", "task_2", etc. for subagent tasks.
	TaskID string `json:"task_id"`

	// ParentID is the parent task ID. Empty for "main".
	ParentID string `json:"parent_id,omitempty"`

	// Type is the event type, used by clients for routing and rendering.
	Type ReactEventType `json:"type"`

	// Data carries the event payload. Its concrete type depends on Type:
	//   - ThinkingDelta: string (text fragment)
	//   - ThinkingDone: Thought
	//   - ActionStart: ActionStartData
	//   - ActionProgress: ActionProgressData
	//   - ToolExecStart: ToolExecStartData
	//   - ToolExecEnd: ToolExecEndData
	//   - ActionEnd: ActionEndData
	//   - ObservationDone: Observation
	//   - SubtaskSpawned: SubtaskInfo
	//   - SubtaskCompleted: SubtaskResult
	//   - FinalAnswer: string
	//   - ClarifyNeeded: string (the question)
	//   - PermissionRequest: PermissionRequestData
	//   - PermissionDenied: string (denial reason)
	//   - ExecutionSummary: ExecutionSummaryData
	//   - Error: string (error message)
	//   - CycleEnd: CycleInfo
	Data any `json:"data,omitempty"`

	// Timestamp is when the event was created.
	Timestamp int64 `json:"timestamp"`
}

// ActionStartData is the payload for ActionStart events (action-level, not per-tool).
type ActionStartData struct {
	ToolCount            int      `json:"tool_count"`
	ToolNames            []string `json:"tool_names"`
	TotalPredictedTokens int      `json:"total_predicted_tokens,omitempty"`
	Iteration            int      `json:"iteration,omitempty"`
}

// ToolExecStartData is the payload for ToolExecStart events.
type ToolExecStartData struct {
	ToolName       string         `json:"tool_name"`
	Params         map[string]any `json:"params,omitempty"`
	PredictedTokens int           `json:"predicted_tokens,omitempty"`
}

// ToolExecEndData is the payload for ToolExecEnd events.
type ToolExecEndData struct {
	ToolName string        `json:"tool_name"`
	Success  bool          `json:"success"`
	Result   string        `json:"result,omitempty"`
	Error    string        `json:"error,omitempty"`
	Duration time.Duration `json:"duration_ms"`
}

// ActionEndData is the payload for ActionEnd events.
type ActionEndData struct {
	TotalTools   int    `json:"total_tools"`
	SuccessCount int    `json:"success_count"`
	FailedCount  int    `json:"failed_count"`
	Summary      string `json:"summary,omitempty"`
}

// ActionProgressData is the payload for ActionProgress events.
type ActionProgressData struct {
	CompletedCount int    `json:"completed_count"`
	TotalCount     int    `json:"total_count"`
	Status         string `json:"status,omitempty"`
}

// SubtaskInfo is the payload for SubtaskSpawned events.
type SubtaskInfo struct {
	TaskID      string `json:"task_id"`
	AgentName   string `json:"agent_name,omitempty"`
	Description string `json:"description"`
	Timeout     string `json:"timeout,omitempty"`
}

// SubtaskResult is the payload for SubtaskCompleted events.
type SubtaskResult struct {
	TaskID  string `json:"task_id"`
	Success bool   `json:"success"`
	Answer  string `json:"answer,omitempty"`
	Error   string `json:"error,omitempty"`
}

// CycleInfo is the payload for CycleEnd events.
type CycleInfo struct {
	Iteration         int           `json:"iteration"`
	TerminationReason string        `json:"termination_reason,omitempty"`
	Duration          time.Duration `json:"duration_ms"`
}

// PermissionRequestData is the payload for PermissionRequest events.
type PermissionRequestData struct {
	ToolName    string         `json:"tool_name"`
	Params      map[string]any `json:"params,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	SecurityLevel SecurityLevel `json:"security_level"`
}

// ExecutionSummaryData is the payload for ExecutionSummary events.
type ExecutionSummaryData struct {
	TotalIterations int            `json:"total_iterations"`
	ToolCalls       int            `json:"tool_calls"`
	ToolsUsed       []string       `json:"tools_used,omitempty"`
	TotalDuration   time.Duration  `json:"total_duration_ms"`
	TokensUsed      int            `json:"tokens_used"`
	TerminationReason string       `json:"termination_reason,omitempty"`
}

// NewReactEvent creates a new ReactEvent with the current timestamp.
func NewReactEvent(sessionID, taskID, parentID string, eventType ReactEventType, data any) ReactEvent {
	return ReactEvent{
		SessionID: sessionID,
		TaskID:    taskID,
		ParentID:  parentID,
		Type:      eventType,
		Data:      data,
		Timestamp: time.Now().UnixMilli(),
	}
}

// TaskSummaryData is the payload for TaskSummary events.
// It carries a natural-language summary of the task execution produced by the LLM.
type TaskSummaryData struct {
	Summary      string `json:"summary"`       // Natural-language task execution summary
	InputTokens  int    `json:"input_tokens"`   // Estimated input tokens for this LLM call
	OutputTokens int    `json:"output_tokens"`  // Reported output tokens from LLM provider
}
