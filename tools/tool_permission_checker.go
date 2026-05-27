package tools

// ToolPermissionChecker determines whether a tool execution is permitted.
type ToolPermissionChecker interface {
	CheckPermissions(ctx *ToolUseContext) PermissionResult
}
