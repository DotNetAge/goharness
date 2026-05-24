package tools

import (
	"fmt"

	"github.com/DotNetAge/goreact/core"
)

type fallbackPermissionChecker struct{}

func NewFallbackPermissionChecker() core.ToolPermissionChecker {
	return &fallbackPermissionChecker{}
}

func (c *fallbackPermissionChecker) CheckPermissions(ctx *core.ToolUseContext) core.PermissionResult {
	info := ctx.ToolInfo
	if info != nil && (info.SecurityLevel == core.LevelSafe || info.IsReadOnly) {
		return core.PermissionResult{Behavior: core.PermissionAllow}
	}
	reason := fmt.Sprintf("Tool %q requires your authorization", ctx.ToolName)
	if info != nil && info.SecurityLevel == core.LevelHighRisk {
		reason = fmt.Sprintf("Tool %q is high-risk and requires your explicit authorization", ctx.ToolName)
	}
	return core.PermissionResult{
		Behavior: core.PermissionAsk,
		Message:  reason,
	}
}
