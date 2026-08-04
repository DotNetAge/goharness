package session

// PendingPermission 捕获正在等待用户决策的工具调用。
// 它持有运行时实际执行工具（Allow）或合成"权限拒绝"工具
// 结果（Deny）所需的全部信息，而 LLM 永远不会看到 "ask"
// 这个中间状态。
type PendingPermission struct {
	// ToolName 是已注册工具的名称（例如 "Bash"、"Write"）。
	ToolName string

	// ToolCallID 与产生此次调用的助手消息上的 ToolCall.ID 匹配。
	// 合成的"权限拒绝"结果（或实际执行工具的结果）会以此 ID
	// 追加到会话中，以满足 OpenAI 的严格契约：每个 tool_call
	// 都必须有对应的 tool 消息。
	ToolCallID string

	// Arguments 是最初传给工具的参数 map。当用户允许时，
	// 运行时使用这些精确参数重新调用工具 —— 不会重新推导。
	Arguments map[string]any

	// Reason 是已经在 UI 中展示的可读说明
	// （例如 "command contains 'rm -rf /'"）。它会被复用到
	// "权限拒绝"工具结果中，让 LLM 看到调用被拒的原因。
	Reason string

	// SecurityLevel 保留自工具的 ToolInfo，以便 UI
	//（以及未来的审计日志）能渲染正确的严重级别徽章。
	SecurityLevel string
}
