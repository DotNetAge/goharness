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

// ── Priority 常量 ────────────────────────────────────────────────────────────

const (
	// Priority constants — built-in hooks occupy 40-49 on each chain.
	// 50-59 reserved for future built-in hooks.
	// User hooks use 0-39 (before built-in) or 61-100 (after built-in).
	PriorityPreCheck          = 40
	PriorityPermission        = 41
	PriorityThoughtEvent      = 42
	PriorityToolEvent         = 43
	PriorityObservationEvent  = 44
	PriorityThoughtLogger     = 45
	PriorityToolLogger        = 46
	PriorityObservationLogger = 47
	PriorityBudget            = 48
	PriorityConvergence       = 49
)

// ── HookResult ──────────────────────────────────────────────────────────────

// HookResult 控制流返回值。
// Before/After 方法返回此类型，有三种结果：
//   - Abort: 终止当前阶段的后续执行（作用域见 §3.5）
//   - Error: 使当前操作失败（当前工具/阶段不继续）
//   - 零值: 继续正常流程
type HookResult struct {
	Abort       bool
	AbortReason string
	Error       error
}

// IsTerminal returns true when the HookResult signals an abort or error.
func (r HookResult) IsTerminal() bool {
	return r.Abort || r.Error != nil
}

// ── Hook 接口 ───────────────────────────────────────────────────────────────

// ThoughtHook 思考阶段钩子。
// Before 在 LLM 调用前执行，可修改 CallInput。
// After 在 LLM 返回后执行，可修改 Thought。
// Abort 在终止时反向通知清理。
type ThoughtHook interface {
	Priority() int
	Before(ctx *ReactContext, input *CallInput) HookResult
	After(ctx *ReactContext, thought *Thought) HookResult
	Abort(ctx *ReactContext, reason string)
}

// ToolHook 工具执行阶段钩子（工具级别粒度）。
// Before 在单个工具执行前执行，只做 Allow/Deny（不修改 params）。
// After 在单个工具执行后执行，可修改 ToolResult。
// Abort 只跳过当前工具，不终止整个循环。
type ToolHook interface {
	Priority() int
	Before(ctx *ReactContext, toolName string, params map[string]any) HookResult
	After(ctx *ReactContext, result *ToolResult) HookResult
	Abort(ctx *ReactContext, reason string)
}

// ObservationHook 观察阶段钩子。
// 没有 Before 方法——观察是 post-hoc 分析。
// After 在观察结论产生后执行，可修改 Observation 并判定终止。
type ObservationHook interface {
	Priority() int
	After(ctx *ReactContext, obs *Observation) HookResult
	Abort(ctx *ReactContext, reason string)
}

// ── 原有接口 ────────────────────────────────────────────────────────────────

// RegistryHub defines the contract for accessing tool, skill, and rule registries.
// It provides a unified entry point for all registry operations and tool registration.
//
// Implementations must be thread-safe for concurrent access.
type RegistryHub interface {
	// SkillRegistry returns the skill registry for loading and querying skills.
	SkillRegistry() core.SkillRegistry

	// ToolRegistry returns the tool registry for tool lookup and discovery.
	ToolRegistry() core.ToolRegistry

	// ToolExecutor returns the configured tool executor with permission chain.
	ToolExecutor() core.ToolExecutor

	// RuleRegistry returns the rule registry (may be nil if not configured).
	RuleRegistry() core.RuleRegistry

	// RegisterTool adds a new tool to the registry.
	// Returns error if a tool with the same name already exists.
	RegisterTool(tool core.FuncTool) error
}

// SessionManager defines the contract for managing conversation sessions,
// context windows, and token estimation.
//
// The session manager is responsible for:
//   - Maintaining the sliding context window within token limits
//   - Persisting conversation history to the session store
//   - Estimating token counts for content strings
//   - Configuring slide behavior when context exceeds limits
type SessionManager interface {
	// SessionStore returns the backing store for conversation persistence.
	SessionStore() core.SessionStore

	// ContextWindow returns the current context window (may be nil before first call).
	ContextWindow() *core.ContextWindow

	// SetContextWindow replaces the current context window (used for session restore).
	SetContextWindow(cw *core.ContextWindow)

	// SlideConfig returns the current sliding window configuration.
	SlideConfig() core.SlideConfig

	// EstimateTokens estimates the number of tokens in the given content string.
	EstimateTokens(content string) int
}

// TAOExecutor defines the contract for executing the Think-Act-Observe cycle.
//
// Each method corresponds to one phase of the T-A-O loop:
//   - Think: Call LLM and parse response into a Thought
//   - Act: Execute the decision from Thought (tool calls or answer)
//   - Observe: Evaluate results and determine next action
//
// Think returns a TokenUsage with full token breakdown and any error.
// Act and Observe return error only.
type TAOExecutor interface {
	// Think executes the thinking phase: calls LLM, parses response into Thought.
	// Returns full token usage breakdown and any error.
	Think(ctx *ReactContext) (core.TokenUsage, error)

	// Act executes the action phase: performs tool calls or generates answer based on Thought.
	Act(ctx *ReactContext) error

	// Observe executes the observation phase: evaluates Action results, checks termination conditions.
	Observe(ctx *ReactContext) error
}

var _ RegistryHub = (*Reactor)(nil)
var _ SessionManager = (*Reactor)(nil)
var _ TAOExecutor = (*Reactor)(nil)
