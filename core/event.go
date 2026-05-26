package core

import "time"

// ReactEventType identifies the type of agent-level event.
// These are distinct from gochat/core.StreamEventType which operates at the LLM token level.
// ReactEvent operates at the agent business logic level (Think-Act cycles, tool calls, subtasks).
type ReactEventType string

const (
	// ThinkingDelta is a fragment of the Think phase output (streaming).
	ThinkingDelta ReactEventType = "thinking_delta"

	// ContentDelta is a streaming text content fragment from the LLM response.
	ContentDelta ReactEventType = "content_delta"

	// ToolUseDelta is a streaming tool call argument fragment from the LLM response.
	ToolUseDelta ReactEventType = "tool_use_delta"

	// ThinkingDone signals the Think phase has completed and the full thought is available.
	ThinkingDone ReactEventType = "thinking_done"



	// ToolExecStart signals a specific tool is about to start executing.
	ToolExecStart ReactEventType = "tool_exec_start"

	// ToolExecEnd signals a specific tool has completed execution.
	ToolExecEnd ReactEventType = "tool_exec_end"


	// SubtaskSpawned signals a subagent task has been created.
	SubtaskSpawned ReactEventType = "subtask_spawned"

	// SubtaskCompleted signals a subagent task has finished (success or failure).
	SubtaskCompleted ReactEventType = "subtask_completed"

	// AgentTalkStart signals an AgentTalk conversation is about to begin.
	AgentTalkStart ReactEventType = "agent_talk_start"

	// AgentTalkEnd signals an AgentTalk conversation has completed.
	AgentTalkEnd ReactEventType = "agent_talk_end"

	// FinalAnswer signals the Reactor has produced its final answer.
	FinalAnswer ReactEventType = "final_answer"

	// PermissionRequest signals a tool needs user authorization before execution.
	PermissionRequest ReactEventType = "permission_request"

	// PermissionDenied signals a tool execution was denied by the permission system.
	PermissionDenied ReactEventType = "permission_denied"

	// AskUserRequest signals the LLM is asking the user a question or set of questions.
	// Unlike PermissionRequest (security), this is a dialogue interaction.
	AskUserRequest ReactEventType = "ask_user_request"

	// ExecutionSummary signals the reactor has completed and provides usage statistics.
	ExecutionSummary ReactEventType = "execution_summary"

	// Error signals an error at the reactor level.
	Error ReactEventType = "error"

	// LLMTimeout signals the LLM call (Think phase) exceeded its time limit.
	LLMTimeout ReactEventType = "llm_timeout"

	// CycleEnd signals one complete Think-Act cycle has ended.
	CycleEnd ReactEventType = "cycle_end"

	// TaskSummary signals a natural-language summary of the completed task.
	// This is emitted after the Think-Act loop finishes for non-trivial tasks.
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
	//   - ContentDelta: string (text content fragment)
	//   - ToolUseDelta: ToolUseDeltaData
	//   - ThinkingDone: Thought
	//   - ToolExecStart: ToolExecStartData
	//   - ToolExecEnd: ToolExecEndData
	//   - SubtaskSpawned: SubtaskInfo
	//   - SubtaskCompleted: SubtaskResult
	//   - FinalAnswer: string
	//   - PermissionRequest: PermissionRequestData  (Grant/Deny)
	//   - PermissionDenied: string (denial reason)
	//   - AskUserRequest: AskUserRequestData  (Reply)
	//   - ExecutionSummary: ExecutionSummaryData
	//   - Error: string (error message)
	//   - CycleEnd: CycleInfo
	Data any `json:"data,omitempty"`

	// Timestamp is when the event was created.
	Timestamp int64 `json:"timestamp"`
}

// ToolUseDeltaData is the payload for ToolUseDelta events.
type ToolUseDeltaData struct {
	Index     int    `json:"index"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// ToolExecStartData is the payload for ToolExecStart events.
type ToolExecStartData struct {
	ToolName        string         `json:"tool_name"`
	Params          map[string]any `json:"params,omitempty"`
	PredictedTokens int            `json:"predicted_tokens,omitempty"`
}

// ToolExecEndData is the payload for ToolExecEnd events.
type ToolExecEndData struct {
	ToolName   string        `json:"tool_name"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Success    bool          `json:"success"`
	Result     string        `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration_ms"`
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

// AgentTalkInfo is the payload for AgentTalkStart events.
type AgentTalkInfo struct {
	To        string `json:"to"`
	SessionID string `json:"session_id"`
	Message   string `json:"message,omitempty"`
}

// AgentTalkResult is the payload for AgentTalkEnd events.
type AgentTalkResult struct {
	To        string `json:"to"`
	SessionID string `json:"session_id"`
	Reply     string `json:"reply,omitempty"`
	Error     string `json:"error,omitempty"`
}

// CycleInfo is the payload for CycleEnd events.
type CycleInfo struct {
	Iteration         int           `json:"iteration"`
	TerminationReason string        `json:"termination_reason,omitempty"`
	Duration          time.Duration `json:"duration_ms"`
}

// LLMTimeoutData is the payload for LLMTimeout events.
type LLMTimeoutData struct {
	SessionID string        `json:"session_id"`
	Timeout   time.Duration `json:"timeout_ms"`
	Elapsed   time.Duration `json:"elapsed_ms"`
	Error     string        `json:"error,omitempty"`
}

// PermissionRequestData is the payload for PermissionRequest events.
// The embedded grant/deny callbacks allow the event subscriber to respond
// without holding a reference to the AskPermission instance.
type PermissionRequestData struct {
	TickID        string         `json:"tick_id"`
	ToolName      string         `json:"tool_name"`
	Params        map[string]any `json:"params,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	SecurityLevel SecurityLevel  `json:"security_level"`

	grant func(updatedInput map[string]any)
	deny  func(reason string)
}

func (d *PermissionRequestData) Grant(updatedInput map[string]any) {
	if d.grant != nil {
		d.grant(updatedInput)
	}
}

func (d *PermissionRequestData) Deny(reason string) {
	if d.deny != nil {
		d.deny(reason)
	}
}

// AskUserQuestion represents a single question asked by the LLM via the AskUser tool.
// Semantically distinct from PermissionQuestion (which is for tool security approvals).
// The LLM uses this to gather user input — single choice, multiple choice, or free text.
type AskUserQuestion struct {
	Question    string   `json:"question"`              // The question text
	Options     []string `json:"options,omitempty"`     // Answer choices (optional for free-text)
	MultiSelect bool     `json:"multi_select"`          // Allow multiple selections
}

// AskUserRequestData is the payload for AskUserRequest events.
// The LLM asks the user structured questions (single/multi choice, free text).
// The embedded reply callback delivers the user's answers.
type AskUserRequestData struct {
	TickID    string           `json:"tick_id"`
	Questions []AskUserQuestion `json:"questions"`

	reply func(answers map[string]string)
}

func (d *AskUserRequestData) Reply(answers map[string]string) {
	if d.reply != nil {
		d.reply(answers)
	}
}

// ExecutionSummaryData is the payload for ExecutionSummary events.
type ExecutionSummaryData struct {
	TotalIterations   int           `json:"total_iterations"`
	ToolCalls         int           `json:"tool_calls"`
	ToolsUsed         []string      `json:"tools_used,omitempty"`
	TotalDuration     time.Duration `json:"total_duration_ms"`
	TokensUsed        TokenUsage    `json:"tokens_used"`
	TerminationReason string        `json:"termination_reason,omitempty"`
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
	Summary    string     `json:"summary"`              // Natural-language task execution summary
	TokenUsage TokenUsage `json:"token_usage"`           // Full token consumption breakdown
}
