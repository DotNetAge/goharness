package tools

import (
	"context"
)

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

	// Iteration is the current Think-Act cycle number.
	Iteration int

	// Ctx is the context.Context for cancellation support.
	Ctx context.Context
}
