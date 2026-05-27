package hooks

import (
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goreact/session"
)

const (
	PriorityPreCheck    = 40
	PriorityPermission  = 41
	PriorityLoopEvent   = 42
	PriorityToolEvent   = 43
	PriorityLoopLogger  = 45
	PriorityToolLogger  = 46
	PriorityConvergence = 49
)

type HookResult struct {
	Abort       bool
	AbortReason string
	Error       error
}

func (r HookResult) IsTerminal() bool {
	return r.Abort || r.Error != nil
}

type LoopHook interface {
	Priority() int
	BeforeLLM(sessionID string, iteration int, input *CallInput) HookResult
	AfterLLM(sessionID string, iteration int, resp *LLMResponse, results []ToolResult) HookResult
	Abort(sessionID string, reason string)
}

type ToolHook interface {
	Priority() int
	Before(sessionID string, toolName string, params map[string]any) HookResult
	After(result *ToolResult) HookResult
	Abort(reason string)
}

type ConversationHistory = []session.Message

type LLMResponse struct {
	Content      string
	Reasoning    string
	FinishReason string
	ToolCalls    []ToolCallInvocation
	TokenUsage   session.TokenUsage
	AbortReason  string
}

type ToolCallInvocation struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type ToolResult struct {
	ToolName   string        `json:"tool_name"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Result     string        `json:"result,omitempty"`
	Metadata   any           `json:"metadata,omitempty"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration_ns"`
	Success    bool          `json:"success"`
}

type CallInput struct {
	SessionID            string
	SystemPromptSections []gochatcore.Message
	UserMessage          string
	History              []session.Message
	Tools                []gochatcore.Tool
}

func ToolResultSummary(tr ToolResult) string {
	prefix := "[" + tr.ToolName + "]"
	if tr.Error != "" {
		return prefix + " error: " + tr.Error
	}
	if tr.Result != "" {
		truncated := Truncate(tr.Result, 200)
		return prefix + " returned: " + truncated
	}
	return prefix + " returned: (empty result)"
}

func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
