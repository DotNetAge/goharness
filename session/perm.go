package session

// PendingPermission captures the tool call that is waiting for the
// user's decision. It holds everything the runtime needs to either
// actually execute the tool (Allow) or synthesize a "Permission
// Denied" tool result (Deny) without the LLM ever seeing the
// "ask" intermediate state.
type PendingPermission struct {
	// ToolName is the registered tool's name (e.g. "Bash", "Write").
	ToolName string

	// ToolCallID matches the ToolCall.ID on the assistant message that
	// produced this invocation. The synthesized "Permission Denied"
	// result (or the executed tool's result) is appended to the
	// session with this ID, satisfying the strict OpenAI contract that
	// every tool_call must have a matching tool message.
	ToolCallID string

	// Arguments is the parameter map originally passed to the tool. The
	// runtime re-invokes the tool with these exact arguments when the
	// user allows — no re-derivation is done.
	Arguments map[string]any

	// Reason is the human-readable explanation already shown in the UI
	// (e.g. "command contains 'rm -rf /'"). It is re-used for the
	// "Permission Denied" tool result so the LLM can see why the call
	// was rejected.
	Reason string

	// SecurityLevel is preserved from the tool's ToolInfo so the UI
	// (and any future audit log) can render the right severity badge.
	SecurityLevel string
}
