// Package reactor implements the Think-Act-Observe (T-A-O) execution engine
// with progressive disclosure, multi-agent coordination, and lifecycle management.
//
// Reactor is organized into four logical domains:
//
//   RegistryHub  — Tool, skill, intent, and rule registries + executor
//   SessionManager — Context window, session store, slide configuration
//   Lifecycle      — Snapshot, pause, heartbeat management
//   TAOExecutor    — Think → Act → Observe phase execution
//
// These interfaces are satisfied by the *Reactor struct and used to
// decompose the god object into focused contracts.
package reactor

import (
	"github.com/DotNetAge/goreact/core"
)

// RegistryHub provides access to all registries and the tool executor.
type RegistryHub interface {
	SkillRegistry() core.SkillRegistry
	ToolRegistry() core.ToolRegistry
	ToolExecutor() core.ToolExecutor
	RuleRegistry() core.RuleRegistry
	RegisterTool(tool core.FuncTool) error
}

// SessionManager provides context window and session storage management.
type SessionManager interface {
	SessionStore() core.SessionStore
	ContextWindow() *core.ContextWindow
	SetContextWindow(cw *core.ContextWindow)
	SlideConfig() core.SlideConfig
	EstimateTokens(content string) int
}

// Lifecycle handles snapshot, pause, and coordinator control operations.
type Lifecycle interface {
	SetPauseRequested()
	TakeSnapshot() *RunSnapshot
	ConsumeSnapshot() *RunSnapshot
	PeekSnapshot() *RunSnapshot
}

// TAOExecutor provides access to the T-A-O phases for testing and orchestration.
type TAOExecutor interface {
	Think(ctx *ReactContext) (int, int, error)
	Act(ctx *ReactContext) error
	Observe(ctx *ReactContext) error
	CheckTermination(ctx *ReactContext) (bool, string)
}

// Compile-time checks that *Reactor satisfies these interfaces.
var _ RegistryHub = (*Reactor)(nil)
var _ SessionManager = (*Reactor)(nil)
var _ Lifecycle = (*Reactor)(nil)
var _ TAOExecutor = (*Reactor)(nil)
