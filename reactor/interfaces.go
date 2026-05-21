// Package reactor implements the Think-Act-Observe (T-A-O) execution engine
// with progressive disclosure and multi-agent coordination.
//
// Reactor is organized into three logical domains:
//
//   RegistryHub    — Tool, skill, intent, and rule registries + executor
//   SessionManager — Context window, session store, slide configuration
//   TAOExecutor    — Think → Act → Observe phase execution
//
// These interfaces are satisfied by the *Reactor struct and used to
// decompose the god object into focused contracts.
package reactor

import (
	"github.com/DotNetAge/goreact/core"
)

type RegistryHub interface {
	SkillRegistry() core.SkillRegistry
	ToolRegistry() core.ToolRegistry
	ToolExecutor() core.ToolExecutor
	RuleRegistry() core.RuleRegistry
	RegisterTool(tool core.FuncTool) error
}

type SessionManager interface {
	SessionStore() core.SessionStore
	ContextWindow() *core.ContextWindow
	SetContextWindow(cw *core.ContextWindow)
	SlideConfig() core.SlideConfig
	EstimateTokens(content string) int
}

type TAOExecutor interface {
	Think(ctx *ReactContext) (int, int, error)
	Act(ctx *ReactContext) error
	Observe(ctx *ReactContext) error
	CheckTermination(ctx *ReactContext) (bool, string)
}

var _ RegistryHub = (*Reactor)(nil)
var _ SessionManager = (*Reactor)(nil)
var _ TAOExecutor = (*Reactor)(nil)
