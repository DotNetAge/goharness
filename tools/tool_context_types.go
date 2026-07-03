package tools

import (
	"context"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/store"
)

// toolCtxKey is the context key for storing/retrieving the ToolContext.
type toolCtxKeyType struct{}

var toolCtxKey = toolCtxKeyType{}

// ToolContext provides tools with access to runtime dependencies.
//
// All tools receive this via context. The Session pointer is the authoritative
// source for session-level properties (ID, ProjectDir, AgentName, Sponsor, etc.).
// Extracting individual session fields into ToolContext would create leak points
// where copies can go stale or get out of sync with the real source of truth.
type ToolContext struct {
	EmitEvent   func(events.ReactEvent)
	ResultStore *store.ResultStore
	KVStore     store.KVStore
	FileStore   store.FileStore
	Logger      logging.Logger

	// Session is the authoritative source for session-level state.
	// Tools access session properties through its getter methods (ID, ProjectDir, etc.),
	// ensuring thread-safe reads from the single source of truth.
	Session *session.Session

	// SessionWhitelist is a cached (lazy-loaded) reference to the session-level
	// tool whitelist. Grant() methods of PermissionRequired tools check this
	// before prompting the user. Nil means no whitelist is available.
	SessionWhitelist *session.SessionWhitelist
}

// WithToolContext stores a ToolContext in the given context.
func WithToolContext(ctx context.Context, tc *ToolContext) context.Context {
	return context.WithValue(ctx, toolCtxKey, tc)
}

// GetToolContext retrieves the ToolContext from the given context.
// Returns an empty ToolContext if none is set, so callers can safely
// check tc.Session != nil before accessing session properties.
func GetToolContext(ctx context.Context) *ToolContext {
	tc, _ := ctx.Value(toolCtxKey).(*ToolContext)
	if tc == nil {
		return &ToolContext{}
	}
	return tc
}
