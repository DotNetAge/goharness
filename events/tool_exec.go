package events

import "time"

type ToolExecStartData struct {
	ToolName        string         `json:"tool_name"`
	Params          map[string]any `json:"params,omitempty"`
	PredictedTokens int            `json:"predicted_tokens,omitempty"`
}

type ToolExecEndData struct {
	ToolName   string        `json:"tool_name"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Success    bool          `json:"success"`
	Result     string        `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration_ns"`
}
