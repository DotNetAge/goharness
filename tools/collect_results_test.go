package tools

import (
	"testing"

	"github.com/DotNetAge/goharness/session"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFindFinalAnswer_Completed 验证正常完成的子会话返回最终答案。
func TestFindFinalAnswer_Completed(t *testing.T) {
	msgs := []session.Message{
		{Role: "user", Content: "任务"},
		{Role: "assistant", Content: "最终答案: 42"},
	}
	answer, termReason := findFinalAnswer(msgs)
	assert.Equal(t, "最终答案: 42", answer)
	assert.Empty(t, termReason)
}

// TestFindFinalAnswer_CompletedSkipsToolCalls 验证带 tool_calls 的 assistant 消息
// 不被当作最终答案（只能是无 tool_calls 的最终回答）。
func TestFindFinalAnswer_CompletedSkipsToolCalls(t *testing.T) {
	msgs := []session.Message{
		{Role: "user", Content: "任务"},
		{Role: "assistant", Content: "思考中", ToolCalls: []session.ToolCall{{ID: "call_1", Name: "bash"}}},
		{Role: "tool", Content: "工具结果", ToolCallID: "call_1"},
		{Role: "assistant", Content: "最终答案"},
	}
	answer, termReason := findFinalAnswer(msgs)
	assert.Equal(t, "最终答案", answer)
	assert.Empty(t, termReason)
}

// TestFindFinalAnswer_TerminatedMarker 验证无最终答案的终止标记被识别为终止原因。
func TestFindFinalAnswer_TerminatedMarker(t *testing.T) {
	msgs := []session.Message{
		{Role: "user", Content: "任务"},
		{Role: "assistant", Content: SubAgentTerminatedPrefix + " permission_timeout"},
	}
	answer, termReason := findFinalAnswer(msgs)
	assert.Empty(t, answer)
	assert.Equal(t, "permission_timeout", termReason)
}

// TestFindFinalAnswer_ReuseAfterTerminated 验证子会话复用并重新委派后，
// 新轮次的最终答案优先于历史终止标记被识别。
func TestFindFinalAnswer_ReuseAfterTerminated(t *testing.T) {
	msgs := []session.Message{
		{Role: "user", Content: "旧任务"},
		{Role: "assistant", Content: SubAgentTerminatedPrefix + " llm_error"},
		{Role: "user", Content: "继续任务"},
		{Role: "assistant", Content: "重新完成的结果"},
	}
	answer, termReason := findFinalAnswer(msgs)
	require.Empty(t, termReason)
	assert.Equal(t, "重新完成的结果", answer)
}

// TestFindFinalAnswer_ReuseTaskInProgress 验证会话复用后新任务进行中：
// 历史任务的最终答案不得被当作新任务的结果返回（应继续轮询）。
// 这是会话复用（延续上下文）场景的核心边界：旧答案在任务开始标记之前，被边界排除。
func TestFindFinalAnswer_ReuseTaskInProgress(t *testing.T) {
	msgs := []session.Message{
		{Role: "user", Content: "旧任务"},
		{Role: "assistant", Content: "旧任务的结果"},
		{Role: "user", Content: SubAgentTaskStartPrefix + " 新的子任务开始"},
		{Role: "user", Content: "新任务"},
		{Role: "assistant", Content: "思考中", ToolCalls: []session.ToolCall{{ID: "call_1", Name: "bash"}}},
		{Role: "tool", Content: "工具结果", ToolCallID: "call_1"},
	}
	answer, termReason := findFinalAnswer(msgs)
	assert.Empty(t, answer, "新任务尚未完成，不得返回旧任务的结果")
	assert.Empty(t, termReason)
}

// TestFindFinalAnswer_ReuseBeforeQuestionAppended 验证会话复用后、新任务问题尚未
// 追加（spawn 已写入任务开始标记）时：不得命中历史任务的答案，应继续轮询。
// 该窗口由 spawn 在复用会话时追加的任务开始标记（user 角色）封闭。
func TestFindFinalAnswer_ReuseBeforeQuestionAppended(t *testing.T) {
	msgs := []session.Message{
		{Role: "user", Content: "旧任务"},
		{Role: "assistant", Content: "旧任务的结果"},
		{Role: "user", Content: SubAgentTaskStartPrefix + " 新的子任务开始"},
	}
	answer, termReason := findFinalAnswer(msgs)
	assert.Empty(t, answer)
	assert.Empty(t, termReason)
}

// TestFindFinalAnswer_ReuseTaskCompleted 验证会话复用后新任务完成：
// 返回新任务段内的最终答案，而非历史任务的答案。
func TestFindFinalAnswer_ReuseTaskCompleted(t *testing.T) {
	msgs := []session.Message{
		{Role: "user", Content: "旧任务"},
		{Role: "assistant", Content: "旧任务的结果"},
		{Role: "user", Content: SubAgentTaskStartPrefix + " 新的子任务开始"},
		{Role: "user", Content: "新任务"},
		{Role: "assistant", Content: "新任务的结果"},
	}
	answer, termReason := findFinalAnswer(msgs)
	assert.Empty(t, termReason)
	assert.Equal(t, "新任务的结果", answer)
}

// TestFindFinalAnswer_ReuseTaskTerminated 验证会话复用后新任务终止：
// 返回新任务段内的终止标记，而非历史任务的终止标记。
func TestFindFinalAnswer_ReuseTaskTerminated(t *testing.T) {
	msgs := []session.Message{
		{Role: "user", Content: "旧任务"},
		{Role: "assistant", Content: SubAgentTerminatedPrefix + " permission_timeout"},
		{Role: "user", Content: SubAgentTaskStartPrefix + " 新的子任务开始"},
		{Role: "user", Content: "新任务"},
		{Role: "assistant", Content: SubAgentTerminatedPrefix + " llm_error"},
	}
	answer, termReason := findFinalAnswer(msgs)
	assert.Empty(t, answer)
	assert.Equal(t, "llm_error", termReason)
}
