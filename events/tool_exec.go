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
	// ResultMeta 是工具执行结果的旁路统计（如 ±行数、命中数、退出码）。
	// 数据源为工具返回值自带的结构化对象，经 executor 透传，LLM 上下文不受污染。
	// 前端据此渲染工具名片与轮级摘要，无需解析 Result 文本。
	ResultMeta map[string]any `json:"result_meta,omitempty"`
}
