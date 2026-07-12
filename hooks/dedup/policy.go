// Package dedup provides tool execution deduplication for idempotent/read-only tools.
//
// Architecture:
//   - DedupPolicy: public interface for external apps to define custom dedup logic
//   - TryDedup:    shared core logic that checks session history for cache hits
//   - DefaultPolicies: built-in policies for Phase 1 tools
//
// Dedup runs in ToolHook.Before, before permission checks, so cached results
// skip both permission approval and actual tool execution entirely.
package dedup

// DedupPolicy defines the deduplication strategy for a single tool.
// External applications implement this interface to extend dedup
// support to custom tools.
type DedupPolicy interface {
	// ToolName returns the tool name this policy applies to.
	// Must match the tool name used in LLM tool calls.
	ToolName() string

	// ContentKey computes a deterministic hash from tool parameters.
	// Same tool + same logical parameters → same ContentKey.
	// Different parameters → different ContentKey.
	ContentKey(params map[string]any) string
}
