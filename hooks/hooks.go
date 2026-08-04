// Package hooks 提供 gochat 框架的钩子系统。
// 它定义了用于拦截和修改 LLM 循环及工具执行行为的接口与类型。
package hooks

import (
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
)

// 钩子优先级常量定义钩子的执行顺序。
// 数值越小优先级越高（越先执行）。
const (
	// PriorityPermission 是权限检查类钩子的优先级。
	PriorityPermission = 41
	// PriorityLoopLogger 是循环日志类钩子的优先级。
	PriorityLoopLogger = 45
	// PriorityToolLogger 是工具日志类钩子的优先级。
	PriorityToolLogger = 46
	// PriorityConvergence 是收敛检查类钩子的优先级。
	PriorityConvergence = 49
)

// HookResult 表示钩子执行的结果。
// 它指示当前操作是否应中止，并提供可选的错误信息。
type HookResult struct {
	// Abort 指示是否应停止该操作。
	Abort bool
	// AbortReason 包含中止操作的原因。
	AbortReason string
	// Error 包含钩子执行期间发生的任何错误。
	Error error
	// SkipWithResult 设置后，将跳过工具执行并直接使用提供的结果。
	// 由去重钩子使用，用于在不重新执行工具的情况下返回缓存结果。
	SkipWithResult *ToolResult
}

// IsTerminal 当钩子结果表明应停止处理时（由于中止或错误）返回 true。
func (r HookResult) IsTerminal() bool {
	return r.Abort || r.Error != nil
}

// LoopHook 定义拦截 Think-Act 循环的钩子接口。
// 实现可在 LLM 调用前后检查和修改行为，并处理中止场景。
type LoopHook interface {
	Priority() int
	BeforeLLM(sessionID string, iteration int, input *CallInput) HookResult
	AfterLLM(sessionID string, iteration int, resp *LLMResponse, results []ToolResult) HookResult
	Abort(sessionID string, reason string)
}

// ToolHook 定义拦截工具执行的钩子接口。
// 实现可在工具执行前检查权限，并在执行后记录结果。
type ToolHook interface {
	Priority() int
	Before(sessionID string, toolName string, params map[string]any) HookResult
	After(result *ToolResult) HookResult
	Abort(reason string)
}

type ConversationHistory = []session.Message

// LLMResponse 表示 Think 阶段 LLM 调用的响应。
type LLMResponse struct {
	Content      string
	Reasoning    string
	FinishReason string
	ToolCalls    []ToolCallInvocation
	TokenUsage   *session.TokenUsage
	AbortReason  string
}

// ToolCallInvocation 表示 LLM 请求的单次工具调用。
type ToolCallInvocation struct {
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

// ToolResult 表示工具执行的结果。
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

// CallInput 包含 LLM 调用的输入数据。
type CallInput struct {
	SessionID            string
	AgentName            string
	ProjectDir           string // 新增：用于 MemoryThoughtHook 按 ProjectDir 过滤记忆缓冲区
	SystemPromptSections []gochatcore.Message
	UserMessage          string
	History              []session.Message
	Tools                []gochatcore.Tool
}

// ToolResultSummary 返回工具结果的可读摘要。
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

// Truncate 将字符串按 rune 截断到指定最大长度，
// 若发生截断则追加 "..."。
func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
