// Package reactor 实现 ThinkingLoop (TAs: Think-Actions)。
//
// Reactor 组织为两个逻辑域：
//
//	RegistryHub    — 工具、技能、规则注册中心 + 执行器
//	SessionManager — 上下文窗口、会话存储、滑动配置
//
// 循环模型: ThinkingLoop
//
//	iteration < MaxIterations:
//	  1. executeTurn: LLM 调用 → 工具执行 → 完成
//	  2. terminate:   FinishReason 优先，MaxIterations 兜底
package reactor

import (
	"github.com/DotNetAge/goreact/core"
)

// ── Priority 常量 ────────────────────────────────────────────────────────────

const (
	PriorityPreCheck     = 40
	PriorityPermission   = 41
	PriorityLoopEvent    = 42
	PriorityToolEvent    = 43
	PriorityLoopLogger   = 45
	PriorityToolLogger   = 46
	PriorityConvergence  = 49
)

// ── HookResult ──────────────────────────────────────────────────────────────

// HookResult 控制流返回值。
// Before/After 方法返回此类型，有三种结果：
//   - Abort: 终止当前阶段
//   - Error: 使当前操作失败
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

// LoopHook 是 ThinkingLoop 的统一钩子接口。
// 每个迭代触发两个点：
//   - BeforeLLM: LLM 调用前（可修改 CallInput 或中止）
//   - AfterLLM:  LLM 返回后，工具执行前（可检查响应或中止）
//   - Abort:     终止时反向通知清理
type LoopHook interface {
	Priority() int
	BeforeLLM(sessionID string, iteration int, input *CallInput) HookResult
	AfterLLM(sessionID string, iteration int, resp *LLMResponse, results []ToolResult) HookResult
	Abort(sessionID string, reason string)
}

// ToolHook 工具执行阶段钩子（工具级别粒度）。
// Before 在单个工具执行前执行，只做 Allow/Deny（不修改 params）。
// After 在单个工具执行后执行，可修改 ToolResult。
// Abort 只跳过当前工具，不终止整个循环。
type ToolHook interface {
	Priority() int
	Before(sessionID string, toolName string, params map[string]any) HookResult
	After(result *ToolResult) HookResult
	Abort(reason string)
}

// ── RegistryHub ─────────────────────────────────────────────────────────────

// RegistryHub defines the contract for accessing tool, skill, and rule registries.
type RegistryHub interface {
	SkillRegistry() core.SkillRegistry
	ToolRegistry() core.ToolRegistry
	ToolExecutor() core.ToolExecutor
	RuleRegistry() core.RuleRegistry
	RegisterTool(tool core.FuncTool) error
}

// ── SessionManager ──────────────────────────────────────────────────────────

// SessionManager defines the contract for managing conversation sessions,
// context windows, and token estimation.
type SessionManager interface {
	SessionStore() core.SessionStore
	ContextWindow() *core.ContextWindow
	SetContextWindow(cw *core.ContextWindow)
	SlideConfig() core.SlideConfig
}

var _ RegistryHub = (*Reactor)(nil)
var _ SessionManager = (*Reactor)(nil)
