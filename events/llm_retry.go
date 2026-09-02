package events

import "time"

// LLMRetryData 携带 LLM 建流重试的详细信息。
type LLMRetryData struct {
	// SessionID 是发起调用的会话 ID。
	SessionID string `json:"session_id"`

	// Provider 是当前模型的服务商名（如 deepseek / ollama），供前端展示。
	Provider string `json:"provider,omitempty"`

	// Model 是当前模型名。
	Model string `json:"model,omitempty"`

	// StatusCode 是导致重试的 HTTP 状态码（如 429；网络错误为 0）。
	StatusCode int `json:"status_code,omitempty"`

	// Attempt 是即将进行的重试序号（从 1 开始）。
	Attempt int `json:"attempt"`

	// MaxAttempts 是最大重试次数（不含首次请求）。
	MaxAttempts int `json:"max_attempts"`

	// RetryAfter 是本次重试前的退避等待时长，前端据此显示「将于 N 秒后重试」。
	RetryAfter time.Duration `json:"retry_after_ns"`

	// Error 是触发重试的错误信息。
	Error string `json:"error,omitempty"`

	// Phase 为 "retry"（即将重试）或 "recovered"（重试后已成功建流，
	// 前端应自动消除重试警告）。
	Phase string `json:"phase"`
}

// LLM 重试事件的阶段取值。
const (
	// LLMRetryPhaseRetry 表示即将退避等待后重试。
	LLMRetryPhaseRetry = "retry"

	// LLMRetryPhaseRecovered 表示重试后已成功建立流，警告应消除。
	LLMRetryPhaseRecovered = "recovered"
)
