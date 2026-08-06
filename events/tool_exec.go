package events

import "time"

type ToolExecStartData struct {
	ToolName string         `json:"tool_name"`
	Params   map[string]any `json:"params,omitempty"`
}

type ToolExecEndData struct {
	ToolName   string        `json:"tool_name"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Success    bool          `json:"success"`
	Result     string        `json:"result,omitempty"`
	Error      string        `json:"error,omitempty"`
	Duration   time.Duration `json:"duration_ns"`
	// 实际 token 用量：该工具调用所在轮次 LLM 调用的 usage。
	// 由 exec 循环在工具执行完成后回填，供前端「查看结果」展示真实消耗，
	// 而非生成 tool_call 时的预估值。
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
}
