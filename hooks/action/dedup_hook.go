package action

import (
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/hooks/dedup"
	"github.com/DotNetAge/goharness/session"
)

// DedupToolHook wraps a DedupPolicy and hooks into the tool execution pipeline
// to skip redundant tool calls for idempotent/read-only tools.
//
// When the Before hook detects a tool call that matches a previous call with
// the same parameters, it returns a cached result via SkipWithResult and the
// actual tool execution is bypassed entirely.
type DedupToolHook struct {
	priority int
	policy   dedup.DedupPolicy
	session  *session.Session // injected by Runtime.exec() before each loop
}

// NewDedupToolHook creates a DedupToolHook wrapper for the given policy.
// Priority is set at 20 so dedup runs before permission checks (41)
// — a cache hit skips both permission approval and tool execution.
func NewDedupToolHook(policy dedup.DedupPolicy) *DedupToolHook {
	return &DedupToolHook{
		priority: 20,
		policy:   policy,
	}
}

// Priority implements hooks.ToolHook.
func (h *DedupToolHook) Priority() int { return h.priority }

// SetSession injects the current session. Called by Runtime.exec() before
// each Think-Act loop iteration.
func (h *DedupToolHook) SetSession(sess *session.Session) {
	h.session = sess
}

// Before implements hooks.ToolHook.
// It checks whether the current tool call matches a previous one with the
// same parameters. On cache hit it returns a synthetic result; on miss it
// returns an empty HookResult so execution proceeds normally.
func (h *DedupToolHook) Before(_ string, toolName string, params map[string]any) hooks.HookResult {
	if toolName != h.policy.ToolName() {
		return hooks.HookResult{}
	}
	if h.session == nil {
		return hooks.HookResult{}
	}

	result := dedup.TryDedup(h.session, params, h.policy)
	if result != nil {
		return hooks.HookResult{SkipWithResult: result}
	}
	return hooks.HookResult{}
}

// After implements hooks.ToolHook. No-op for dedup.
func (h *DedupToolHook) After(_ *hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

// Abort implements hooks.ToolHook. No-op for dedup.
func (h *DedupToolHook) Abort(_ string) {}

// compile-time interface check
var _ hooks.ToolHook = (*DedupToolHook)(nil)
