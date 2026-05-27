package events

import "time"

type LLMTimeoutData struct {
	SessionID string        `json:"session_id"`
	Timeout   time.Duration `json:"timeout_ns"`
	Elapsed   time.Duration `json:"elapsed_ns"`
	Error     string        `json:"error,omitempty"`
}
