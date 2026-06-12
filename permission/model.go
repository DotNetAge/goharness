package permission

import "github.com/DotNetAge/goharness/tools"

// PermissionResult is an alias for tools.PermissionResult.
// It represents the result of a permission check operation.
type PermissionResult = tools.PermissionResult

// PermissionBehavior is an alias for tools.PermissionBehavior.
// It defines the possible outcomes of a permission check.
type PermissionBehavior = tools.PermissionBehavior

// PermissionAllow indicates that the tool usage is permitted.
const PermissionAllow = tools.PermissionAllow

// PermissionDeny indicates that the tool usage is forbidden.
const PermissionDeny = tools.PermissionDeny

// PermissionAsk indicates that the tool usage requires user approval.
const PermissionAsk = tools.PermissionAsk
