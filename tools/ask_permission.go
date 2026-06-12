package tools

import (
	"fmt"

	"github.com/DotNetAge/goharness/events"
)

type fallbackPermissionChecker struct{}

func NewFallbackPermissionChecker() ToolPermissionChecker {
	return &fallbackPermissionChecker{}
}

func (c *fallbackPermissionChecker) CheckPermissions(ctx *ToolUseContext) PermissionResult {
	info := ctx.ToolInfo
	if info != nil && (info.SecurityLevel == events.LevelSafe || info.IsReadOnly) {
		return PermissionResult{Behavior: PermissionAllow}
	}
	reason := fmt.Sprintf("Tool %q requires your authorization", ctx.ToolName)
	if info != nil && info.SecurityLevel == events.LevelHighRisk {
		reason = fmt.Sprintf("Tool %q is high-risk and requires your explicit authorization", ctx.ToolName)
	}
	return PermissionResult{
		Behavior: PermissionAsk,
		Message:  reason,
	}
}
