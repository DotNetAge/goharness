package agents

import (
	"encoding/json"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/tools"
)

// parseToolInvocations 将 LLM 工具调用对象转换为内部 hook 格式。
//
// 过滤掉无效调用（缺少 ID 或名称）。参数解析规则：
//   - 合法 JSON：反序列化为 map[string]any
//   - 非法 JSON：保存为 {"raw_args": original_string} 以保留原始数据
func parseToolInvocations(calls []gochatcore.ToolCall) []hooks.ToolCallInvocation {
	if len(calls) == 0 {
		return nil
	}
	invocs := make([]hooks.ToolCallInvocation, 0, len(calls))
	for _, tc := range calls {
		if tc.ID == "" || tc.Name == "" {
			continue
		}
		var params map[string]any
		if tc.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Arguments), &params); err != nil {
				params = map[string]any{"raw_args": tc.Arguments}
			}
		}
		invocs = append(invocs, hooks.ToolCallInvocation{
			ID:        tc.ID,
			Name:      tc.Name,
			Arguments: params,
		})
	}
	return invocs
}

// failedToolResult 为工具执行错误或中止创建一个失败的 ToolResult。
//
// 参数：
//   - toolName: 失败的工具名称
//   - toolCallID: 来自 LLM 响应的工具调用 ID
//   - errMsg: 人类可读的错误信息
//   - start: 执行开始时间戳（用于计算耗时）
func failedToolResult(toolName, toolCallID, errMsg string, start time.Time) hooks.ToolResult {
	return hooks.ToolResult{
		ToolName:   toolName,
		ToolCallID: toolCallID,
		Success:    false,
		Error:      errMsg,
		Duration:   time.Since(start),
	}
}

// buildToolResult 根据工具执行输出或错误构造 ToolResult。
//
// 参数：
//   - inv: 原始工具调用（名称、ID、参数）
//   - execResult: ToolExecutor 的执行结果（出错时可能为 nil）
//   - execErr: 工具执行错误（成功时为 nil）
//   - start: 开始时间戳（用于计算耗时）
func buildToolResult(inv hooks.ToolCallInvocation, execResult *tools.ToolExecutionResult, execErr error, start time.Time) hooks.ToolResult {
	tr := hooks.ToolResult{
		ToolName:   inv.Name,
		ToolCallID: inv.ID,
		Duration:   time.Since(start),
	}
	if execErr != nil {
		tr.Error = execErr.Error()
		tr.Success = false
	} else if execResult != nil {
		tr.Result = execResult.Result
		tr.Metadata = execResult.Metadata
		// 透传工具返回的图片数据，供 ToolHook.After（ImageHook）提取后
		// 以 image_url 消息的形式进入上下文。
		tr.Images = execResult.Images
		tr.Duration = execResult.Duration
		tr.Success = execResult.Error == nil
		if execResult.Error != nil {
			tr.Error = execResult.Error.Error()
		}
	}
	return tr
}

// findAskUserInvocation 在调用列表中查找 AskUser 工具调用。
// 若未找到则返回 nil。
func findAskUserInvocation(invocs []hooks.ToolCallInvocation) *hooks.ToolCallInvocation {
	for i := range invocs {
		if invocs[i].Name == "AskUser" {
			return &invocs[i]
		}
	}
	return nil
}

// buildAskUserPendingData 从 AskUser 参数中提取问题数据，并构造供前端展示的 AskUserPendingData。
func buildAskUserPendingData(args map[string]any) events.AskUserPendingData {
	question, _ := args["question"].(string)
	multi, _ := args["multiSelect"].(bool)

	q := events.AskUserQuestion{
		Question:    question,
		MultiSelect: multi,
	}
	if opts, ok := args["options"].([]any); ok {
		for _, o := range opts {
			if s, ok := o.(string); ok {
				q.Options = append(q.Options, s)
			}
		}
	}
	return events.NewAskUserPendingData([]events.AskUserQuestion{q})
}

// buildParamSchema 将 Parameter 切片转换为 LLM 工具定义所需的 JSON Schema 格式。
//
// 生成标准 JSON Schema 的 "object" 类型，每个参数对应一个 property。
func buildParamSchema(params []tools.Parameter) json.RawMessage {
	if len(params) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
	props := schema["properties"].(map[string]any)
	for _, p := range params {
		prop := map[string]any{
			"type":        paramTypeToJSONType(p.Type),
			"description": p.Description,
		}
		if len(p.Enum) > 0 {
			prop["enum"] = p.Enum
		}
		props[p.Name] = prop
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return b
}

// paramTypeToJSONType 将 Go/工具参数类型字符串映射为 JSON Schema 类型名。
//
// 映射规则：
//   - "integer", "int", "int64", "int32" → "integer"
//   - "number", "float64", "float32" → "number"
//   - "boolean", "bool" → "boolean"
//   - "array", "[]string", "[]int" → "array"
//   - "object", "map" → "object"
//   - 其他 → "string"（默认值）
func paramTypeToJSONType(t string) string {
	switch t {
	case "integer", "int", "int64", "int32":
		return "integer"
	case "number", "float64", "float32":
		return "number"
	case "boolean", "bool":
		return "boolean"
	case "array", "[]string", "[]int":
		return "array"
	case "object", "map":
		return "object"
	default:
		return "string"
	}
}
