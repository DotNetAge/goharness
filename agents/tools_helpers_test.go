package agents

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestParseToolInvocationsValid 验证合法工具调用被正确解析。
func TestParseToolInvocationsValid(t *testing.T) {
	calls := []gochatcore.ToolCall{
		{ID: "tc1", Name: "Grep", Arguments: `{"pattern":"foo"}`},
		{ID: "tc2", Name: "Glob", Arguments: `{"pattern":"*.go"}`},
	}
	invocs := parseToolInvocations(calls)
	require.Len(t, invocs, 2)
	assert.Equal(t, "tc1", invocs[0].ID)
	assert.Equal(t, "Grep", invocs[0].Name)
	assert.Equal(t, "foo", invocs[0].Arguments["pattern"])
}

// TestParseToolInvocationsInvalidJSON 验证非法 JSON 参数回退到 raw_args。
func TestParseToolInvocationsInvalidJSON(t *testing.T) {
	calls := []gochatcore.ToolCall{
		{ID: "tc1", Name: "Grep", Arguments: `not json`},
	}
	invocs := parseToolInvocations(calls)
	require.Len(t, invocs, 1)
	assert.Equal(t, "not json", invocs[0].Arguments["raw_args"])
}

// TestParseToolInvocationsMissingFields 验证缺少 ID 或名称的调用被过滤。
func TestParseToolInvocationsMissingFields(t *testing.T) {
	calls := []gochatcore.ToolCall{
		{ID: "", Name: "Grep", Arguments: `{}`},
		{ID: "tc1", Name: "", Arguments: `{}`},
		{ID: "tc2", Name: "Glob", Arguments: `{}`},
	}
	invocs := parseToolInvocations(calls)
	require.Len(t, invocs, 1)
	assert.Equal(t, "tc2", invocs[0].ID)
}

// TestParseToolInvocationsEmptyArguments 验证空参数得到空 map。
func TestParseToolInvocationsEmptyArguments(t *testing.T) {
	calls := []gochatcore.ToolCall{
		{ID: "tc1", Name: "Grep", Arguments: ""},
	}
	invocs := parseToolInvocations(calls)
	require.Len(t, invocs, 1)
	assert.Empty(t, invocs[0].Arguments)
}

// TestParseToolInvocationsEmptySlice 验证空切片返回 nil。
func TestParseToolInvocationsEmptySlice(t *testing.T) {
	invocs := parseToolInvocations(nil)
	assert.Nil(t, invocs)
}

// TestBuildToolResultSuccess 验证成功执行的 ToolResult 构造。
func TestBuildToolResultSuccess(t *testing.T) {
	inv := hooks.ToolCallInvocation{ID: "tc1", Name: "Grep"}
	execResult := &tools.ToolExecutionResult{
		Result:   "result",
		Metadata: map[string]any{"key": "value"},
		Duration: 5 * time.Millisecond,
	}
	tr := buildToolResult(inv, execResult, nil, time.Now().Add(-10*time.Millisecond))
	assert.True(t, tr.Success)
	assert.Equal(t, "result", tr.Result)
	meta := tr.Metadata.(map[string]any)
	assert.Equal(t, "value", meta["key"])
	assert.Empty(t, tr.Error)
}

// TestBuildToolResultExecutionError 验证执行错误的 ToolResult 构造。
func TestBuildToolResultExecutionError(t *testing.T) {
	inv := hooks.ToolCallInvocation{ID: "tc1", Name: "Grep"}
	tr := buildToolResult(inv, nil, errors.New("exec failed"), time.Now())
	assert.False(t, tr.Success)
	assert.Equal(t, "exec failed", tr.Error)
}

// TestBuildToolResultResultError 验证结果对象中包含错误时的处理。
func TestBuildToolResultResultError(t *testing.T) {
	inv := hooks.ToolCallInvocation{ID: "tc1", Name: "Grep"}
	execResult := &tools.ToolExecutionResult{
		Result: "partial",
		Error:  errors.New("result error"),
	}
	tr := buildToolResult(inv, execResult, nil, time.Now())
	assert.False(t, tr.Success)
	assert.Equal(t, "result error", tr.Error)
	assert.Equal(t, "partial", tr.Result)
}

// TestFailedToolResult 验证失败工具结果包含错误信息。
func TestFailedToolResult(t *testing.T) {
	start := time.Now().Add(-20 * time.Millisecond)
	tr := failedToolResult("Grep", "tc1", "timeout", start)
	assert.False(t, tr.Success)
	assert.Equal(t, "timeout", tr.Error)
	assert.Equal(t, "Grep", tr.ToolName)
	assert.Equal(t, "tc1", tr.ToolCallID)
	assert.True(t, tr.Duration >= 20*time.Millisecond)
}

// TestFindAskUserInvocation 验证在调用列表中查找 AskUser。
func TestFindAskUserInvocation(t *testing.T) {
	invocs := []hooks.ToolCallInvocation{
		{ID: "tc1", Name: "Grep"},
		{ID: "tc2", Name: "AskUser"},
	}
	found := findAskUserInvocation(invocs)
	require.NotNil(t, found)
	assert.Equal(t, "tc2", found.ID)
}

// TestFindAskUserInvocationNotFound 验证未找到 AskUser 时返回 nil。
func TestFindAskUserInvocationNotFound(t *testing.T) {
	invocs := []hooks.ToolCallInvocation{
		{ID: "tc1", Name: "Grep"},
	}
	found := findAskUserInvocation(invocs)
	assert.Nil(t, found)
}

// TestBuildAskUserPendingData 验证从参数构造 AskUserPendingData。
func TestBuildAskUserPendingData(t *testing.T) {
	args := map[string]any{
		"question":    "choose one",
		"multiSelect": true,
		"options":     []any{"a", "b", "c"},
	}
	data := buildAskUserPendingData(args)
	require.Len(t, data.Questions, 1)
	assert.Equal(t, "choose one", data.Questions[0].Question)
	assert.True(t, data.Questions[0].MultiSelect)
	assert.Equal(t, []string{"a", "b", "c"}, data.Questions[0].Options)
}

// TestBuildParamSchema 验证参数 schema 构造。
func TestBuildParamSchema(t *testing.T) {
	params := []tools.Parameter{
		{Name: "name", Type: "string", Description: "name desc"},
		{Name: "count", Type: "integer", Description: "count desc"},
		{Name: "status", Type: "string", Description: "status desc", Enum: []any{"a", "b"}},
	}
	raw := buildParamSchema(params)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(raw, &schema))

	props := schema["properties"].(map[string]any)
	assert.Equal(t, "string", props["name"].(map[string]any)["type"])
	assert.Equal(t, "integer", props["count"].(map[string]any)["type"])
	assert.Equal(t, []any{"a", "b"}, props["status"].(map[string]any)["enum"])
	assert.Equal(t, "object", schema["type"])
}

// TestBuildParamSchemaEmpty 验证空参数返回默认 schema。
func TestBuildParamSchemaEmpty(t *testing.T) {
	raw := buildParamSchema(nil)
	assert.JSONEq(t, `{"type":"object","properties":{}}`, string(raw))
}

// TestParamTypeToJSONType 验证类型映射。
func TestParamTypeToJSONType(t *testing.T) {
	cases := map[string]string{
		"integer": "integer",
		"int":     "integer",
		"int64":   "integer",
		"number":  "number",
		"float64": "number",
		"boolean": "boolean",
		"bool":    "boolean",
		"array":   "array",
		"[]string":"array",
		"object":  "object",
		"map":     "object",
		"string":  "string",
		"unknown": "string",
	}
	for input, expected := range cases {
		assert.Equal(t, expected, paramTypeToJSONType(input), "input: %s", input)
	}
}
