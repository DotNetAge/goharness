package core

import "context"

type toolCtxKeyType struct{}

var toolCtxKey toolCtxKeyType

// ToolContext provides runtime context for tool execution.
// It is injected into FuncTool implementations so they can access shared services
// (event bus, result store, KV store, file store) and emit events.
//
// Directory Context (Design-time Safety Guarantee):
//
//   - ProjectDir: ALWAYS populated (defaults to os.Getwd() at Agent creation time)
//     Set via: goreact.WithProjectDir("/path") or auto-captured if omitted
//     Used by: edit/read/write tools to locate project files
//     Visible to: LLM via Environment section in system prompt
//
//   - SessionDir: Populated when SessionStore is available or explicitly set
//     Set via: goreact.WithSessionDir("/path") or auto-resolved from SessionStore
//     Used by: tools that need session-scoped file isolation
//     Visible to: LLM via Environment section in system prompt
//
// IMPORTANT: These fields are NOT "optional hints" — they are design-time safety
// guarantees that prevent runtime failures where LLM lacks directory context.
type ToolContext struct {
	EmitEvent   func(ReactEvent)
	ResultStore *ResultStore
	KVStore     KVStore
	FileStore   FileStore
	SessionID   string
	Logger      Logger // Unified logging interface (replaces fmt.Println)

	// Directory context (Design-time safety: guaranteed by Agent/Reactor layer)
	ProjectDir string // Layer 2: Project working directory (always non-empty after Agent init)
	SessionDir string // Layer 3: Session sandbox directory (when available)
}

// WithToolContext injects a ToolContext into the given context.
func WithToolContext(ctx context.Context, tc *ToolContext) context.Context {
	return context.WithValue(ctx, toolCtxKey, tc)
}

// GetToolContext extracts the ToolContext from context.
// Returns a safe default (empty ToolContext) if not set, preventing nil pointer panics.
//
// Design-time safety: This ensures tools never receive nil, even in test environments
// or when called outside the normal Agent/Reactor execution flow.
// The returned empty ToolContext will cause ResolveTargetPath to use os.Getwd() fallback.
func GetToolContext(ctx context.Context) *ToolContext {
	tc, _ := ctx.Value(toolCtxKey).(*ToolContext)
	if tc == nil {
		return &ToolContext{} // Safe default to prevent nil pointer dereference
	}
	return tc
}
