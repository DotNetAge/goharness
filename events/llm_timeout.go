package events

import "time"

type LLMTimeoutData struct {
	SessionID string        `json:"session_id"`
	Timeout   time.Duration `json:"timeout_ns"`
	Elapsed   time.Duration `json:"elapsed_ns"`
	Error     string        `json:"error,omitempty"`
}

// LLMCancelledData 记录用户取消 LLM 调用的详情。
// 与 LLMTimeout（真实超时）不同，这是用户主动发起的中断。
type LLMCancelledData struct {
	SessionID string        `json:"session_id"`
	Elapsed   time.Duration `json:"elapsed_ns"`
}
