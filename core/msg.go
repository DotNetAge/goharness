package core

// ToolCall represents a tool invocation in an assistant message.
// Mirrors gochat/core.ToolCall for wire-format compatibility.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded argument map
}

// Message represents a single message in a conversation.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Timestamp  int64      `json:"timestamp"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool call ID for role="tool" messages (required by strict APIs like DeepSeek)
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // tool invocations on role="assistant" messages (required by strict APIs like DeepSeek)
}
