package core

import "context"

// PermissionBehavior represents the outcome of a permission check.
type PermissionBehavior string

const (
	// PermissionAllow permits the operation to proceed.
	PermissionAllow PermissionBehavior = "allow"
	// PermissionDeny blocks the operation. The tool will NOT execute.
	PermissionDeny PermissionBehavior = "deny"
	// PermissionAsk suspends execution until the user provides a decision.
	// The permission system will block and wait for an external response.
	PermissionAsk PermissionBehavior = "ask"
)

// QuestionOption represents a single choice in a permission question.
type QuestionOption struct {
	Label       string `json:"label"`       // Display text for the option
	Description string `json:"description"` // Explanation of this choice's implications
	Preview     string `json:"preview"`     // Optional side-by-side preview (markdown/HTML)
}

// PermissionQuestion represents a structured question with options.
// Used by AskUser tool; the permission dialog renders this as a form.
type PermissionQuestion struct {
	Question    string           `json:"question"`     // The question text
	Header      string           `json:"header"`       // Short chip/tag label (max 12 chars)
	Options     []QuestionOption `json:"options"`      // Available choices (2-4 items)
	MultiSelect bool             `json:"multi_select"` // Allow multiple selections
}

// PermissionResult represents the outcome of a permission/authorization check.
type PermissionResult struct {
	Behavior PermissionBehavior `json:"behavior"`

	// Message explains the reason when denied, or the question when asking.
	Message string `json:"message,omitempty"`

	// UpdatedInput allows hooks or user to modify tool parameters before execution.
	// Only meaningful when Behavior is Allow (user approved with modifications).
	UpdatedInput map[string]any `json:"updated_input,omitempty"`
}

// ToolUseContext provides contextual information for permission decisions and hooks.
type ToolUseContext struct {
	// SessionID identifies the conversation session.
	SessionID string

	// TaskID identifies the source task ("main" or subagent task ID).
	TaskID string

	// ToolName is the name of the tool being called.
	ToolName string

	// ToolInfo is the metadata of the tool being called.
	ToolInfo *ToolInfo

	// Params are the original parameters provided by the LLM.
	Params map[string]any

	// Iteration is the current T-A-O cycle number.
	Iteration int

	// Ctx is the context.Context for cancellation support.
	Ctx context.Context
}

// ToolPermissionChecker determines whether a tool execution is permitted.
// Implementations can inspect tool metadata, parameters, and context to make
// authorization decisions.
//
// When CheckPermissions returns PermissionAsk, the executor emits either a
// PermissionRequest event (for tool security) or an AskUserRequest event
// (for LLM-user dialogue), depending on the tool. Both events carry embedded
// callbacks (Grant/Deny/Reply) so subscribers can respond without holding a
// reference to any specific permission instance.
//
// This interface is designed to be composable: a chain of checkers can be
// combined, where each checker's result feeds into the next.
type ToolPermissionChecker interface {
	// CheckPermissions evaluates whether the tool call should be allowed.
	// The returned PermissionResult may have Behavior=Ask, in which case
	// the executor will emit an event and wait for the embedded callback.
	CheckPermissions(ctx *ToolUseContext) PermissionResult
}
