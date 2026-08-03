package agents

import (
	"strings"

	"github.com/DotNetAge/goharness/session"
)

// stripOrphanedToolCalls 移除助手消息中没有对应 tool 响应的孤立 tool_call。
// 这可以防止思考循环被取消时，助手的 tool_call 消息已持久化但工具结果未写入，
// 导致后续 LLM 请求因严格校验而失败。
func stripOrphanedToolCalls(history []session.Message) []session.Message {
	// 收集所有有 tool 响应的 tool_call_id
	toolCallIDs := make(map[string]bool)
	for _, m := range history {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolCallIDs[m.ToolCallID] = true
		}
	}

	result := make([]session.Message, 0, len(history))
	for _, m := range history {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			kept := make([]session.ToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				if toolCallIDs[tc.ID] {
					kept = append(kept, tc)
				}
			}

			// 过滤后若没有任何 tool_call，且文本内容为空，则丢弃整条消息。
			if len(kept) == 0 && strings.TrimSpace(m.Content) == "" {
				continue
			}
			if len(kept) == 0 {
				m.ToolCalls = nil
			} else {
				m.ToolCalls = kept
			}
			result = append(result, m)
		} else {
			result = append(result, m)
		}
	}
	return result
}
