package filestate

// ErrorCode 是 ToolError 的机器可读错误码。
// 见 FILE_TOOLS_ARCHITECTURE.md 公约 2a（审计 M5）。
type ErrorCode string

const (
	// ErrCodeActionable 表示模型可自纠正的错误（路径错了、没 Read、文件过期等）。
	// Runtime 应将其作为 tool 失败返回给模型，由模型尝试修正后重试。
	ErrCodeActionable ErrorCode = "ACTIONABLE"

	// ErrCodeFatal 表示系统故障（磁盘满、权限被撤销等）。
	// Runtime 不应重试，应报告给用户。
	ErrCodeFatal ErrorCode = "FATAL"
)

// ToolError 是文件工具的统一错误类型。
// Message 面向模型（含"描述。行为指令"格式），Internal 面向日志。
type ToolError struct {
	Code     ErrorCode `json:"code"`
	Message  string    `json:"message"`
	Internal string    `json:"-"`
}

func (e *ToolError) Error() string {
	return e.Message
}
