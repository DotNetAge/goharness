// Package hooks provides the hook system for the gochat framework.
// It defines interfaces and types for intercepting and modifying
// the behavior of LLM loops and tool executions.
package hooks

import (
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
)

// Hook priority constants define the execution order of hooks.
// Lower values indicate higher priority (executed first).
const (
	// PriorityPermission is the priority for permission-checking hooks.
	PriorityPermission = 41
	// PriorityLoopLogger is the priority for loop logging hooks.
	PriorityLoopLogger = 45
	// PriorityToolLogger is the priority for tool logging hooks.
	PriorityToolLogger = 46
	// PriorityConvergence is the priority for convergence-checking hooks.
	PriorityConvergence = 49
)

// HookResult represents the result of a hook execution.
// It indicates whether the current operation should be aborted
// and provides optional error information.
type HookResult struct {
	// Abort indicates whether the operation should be stopped.
	Abort bool
	// AbortReason contains the reason for aborting the operation.
	AbortReason string
	// Error contains any error that occurred during hook execution.
	Error error
	// SkipWithResult, when set, causes the tool execution to be skipped
	// and the provided result to be used directly. Used by dedup hooks
	// to return cached results without re-executing the tool.
	SkipWithResult *ToolResult
}

// IsTerminal returns true if the hook result indicates that
// processing should stop (either due to abort or an error).
func (r HookResult) IsTerminal() bool {
	return r.Abort || r.Error != nil
}

// LoopHook defines the interface for hooks that intercept the Think-Act loop.
// Implementations can inspect and modify behavior before and after LLM calls,
// as well as handle abort scenarios.
type LoopHook interface {
	Priority() int
	BeforeLLM(sessionID string, iteration int, input *CallInput) HookResult
	AfterLLM(sessionID string, iteration int, resp *LLMResponse, results []ToolResult) HookResult
	Abort(sessionID string, reason string)
}

// ToolHook defines the interface for hooks that intercept tool executions.
// Implementations can check permissions before tool execution and log results after.
type ToolHook interface {
	Priority() int
	Before(sessionID string, toolName string, params map[string]any) HookResult
	After(result *ToolResult) HookResult
	Abort(reason string)
}

type ConversationHistory = []session.Message

// LLMResponse represents the response from an LLM call in the Think phase.
type LLMResponse struct {
	Content      string
	Reasoning    string
	FinishReason string
	ToolCalls    []ToolCallInvocation
	TokenUsage   *session.TokenUsage
	AbortReason  string
}

// ToolCallInvocation represents a single tool call requested by the LLM.
type ToolCallInvocation struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolResult represents the result of a tool execution.
type ToolResult struct {
	ToolName   string        `json:"tool_name"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Result     string        `json:"result,omitempty"`
	Metadata   any           `json:"metadata,omitempty"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration_ns"`
	Success    bool          `json:"success"`
	// Images 是工具返回的原始图片数据（如 Read 读取的图片文件）。
	// 由 executor 从工具执行结果中提取，与文本结果分离传递。
	Images []tools.ImageContent `json:"images,omitempty"`
	// ImageBlocks 是 ImageHook 将 Images 转换后的图片内容块。
	// executor 持久化工具结果时，若该字段非空，则以 image_url 消息追加进入上下文，
	// 而非混入工具结果的文本内容。
	ImageBlocks []session.ImageBlock `json:"image_blocks,omitempty"`
}

// CallInput contains the input data for an LLM call.
type CallInput struct {
	SessionID            string
	AgentName            string
	ProjectDir           string // 新增：用于 MemoryThoughtHook 按 ProjectDir 过滤记忆缓冲区
	SystemPromptSections []gochatcore.Message
	UserMessage          string
	History              []session.Message
	Tools                []gochatcore.Tool
}

// ToolResultSummary returns a human-readable summary of a tool result.
func ToolResultSummary(tr ToolResult) string {
	prefix := "[" + tr.ToolName + "]"
	if tr.Error != "" {
		return prefix + " 错误: " + tr.Error
	}
	if tr.Result != "" {
		truncated := Truncate(tr.Result, 200)
		return prefix + " 返回: " + truncated
	}
	return prefix + " 返回: (空结果)"
}

// Truncate truncates a string to the specified maximum length in runes,
// appending "..." if truncated.
func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
