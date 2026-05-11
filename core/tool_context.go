package core

import "context"

type toolCtxKeyType struct{}

var toolCtxKey toolCtxKeyType

// ToolContext carries runtime infrastructure accessible to all tools via context.
// Tools that need special capabilities (e.g., delegate creating sub-agents)
// extract it via GetToolContext(). Tools that don't need it ignore it entirely.
type ToolContext struct {
	EmitEvent   func(ReactEvent)
	ResultStore *ResultStore
	KVStore     KVStore
	FileStore   FileStore
	SessionID   string
	Logger      Logger // Unified logging interface, injected from Agent/ReActor

	// Directory hints (optional, set by application layer like MindX)
	// When empty, tools fall back to os.Getwd() for backward compatibility.
	ProjectDir string // Layer 2: Project working directory
	SessionDir string // Layer 3: Session sandbox directory
}

// WithToolContext injects a ToolContext into the given context.
func WithToolContext(ctx context.Context, tc *ToolContext) context.Context {
	return context.WithValue(ctx, toolCtxKey, tc)
}

// GetToolContext extracts the ToolContext from context.
// Returns nil if not set (tools should handle nil gracefully).
func GetToolContext(ctx context.Context) *ToolContext {
	tc, _ := ctx.Value(toolCtxKey).(*ToolContext)
	return tc
}
