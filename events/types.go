package events

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

	// Compaction signals that the session's context window was compacted
	// (old messages slid out). Fires when the active window exceeds 80% of
	// maxWindowSize and messages are trimmed to ~60%.
	Compaction ReactEventType = "compaction"
)
