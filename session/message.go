package session

// ToolCall represents a tool invocation in an assistant message.
// Mirrors gochat/core.ToolCall for wire-format compatibility.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded argument map
}

// Message represents a single message in a conversation.
// Compatible with OpenAI chat completion format:
//   - role: "system" | "user" | "assistant" | "tool"
//   - content: message text
//   - reasoning_content: thinking/reasoning stream from the model (e.g. DeepSeek-R1)
//   - tool_calls: tool invocations on assistant messages
//   - tool_call_id: correlation ID for tool result messages
type Message struct {
	Role            string     `json:"role"`
	Content         string     `json:"content"`
	ReasoningContent string    `json:"reasoning_content,omitempty"` // thinking stream (DeepSeek-R1 etc.)
	Timestamp       int64      `json:"timestamp"`
	ToolCallID      string     `json:"tool_call_id,omitempty"`      // for role="tool" messages
	ToolCalls       []ToolCall `json:"tool_calls,omitempty"`         // for role="assistant" messages
}
