package agents

import (
	"context"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
)

// toolPairingRepairNotice 是工具调用配对断裂修复后追加的用户说明消息，
// 用于告知 LLM 上一轮已被系统回滚、需要重新规划执行。
const toolPairingRepairNotice = "（系统提示）检测到上一轮的工具调用序列异常，已回滚该轮。请基于当前上下文重新规划并继续完成原任务。"

// isToolPairingError 判断错误是否属于「工具调用配对不完整」类错误。
// 这类错误来自 OpenAI 兼容接口的 400 响应，两种典型文案：
//   - 缺少 tool 消息："insufficient tool messages following tool_calls message" /
//     "must be followed by tool messages"；
//   - tool 消息无主（重复应答或前置 assistant 被回滚）：
//     "Messages with role 'tool' must be a response to a preceding message with 'tool_calls'"。
func isToolPairingError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "insufficient tool messages") ||
		strings.Contains(msg, "must be followed by tool messages") ||
		strings.Contains(msg, "must be a response to a preceding message with")
}

// suspendedToolCallIDs 汇总会话当前挂起中的 tool_call ID。
//
// 两类挂起的共同点：本轮没有工具结果，且结果发生在未来——
//   - AskUser 挂起：结果是用户的回答，由下一轮入口写成 tool 消息；
//   - 待授权工具挂起：结果是用户授权后执行产出，由 applyPermissionAction 写入。
//
// 回写补齐与配对检测都要先认出它们：补齐必须跳过（否则抢占应答位），
// 检测必须豁免（否则等待态被当作断裂回滚）。
func suspendedToolCallIDs(s *session.Session) map[string]struct{} {
	out := make(map[string]struct{}, 2)
	if p := s.PeekPendingAskUser(); p != nil && p.ToolCallID != "" {
		out[p.ToolCallID] = struct{}{}
	}
	if p := s.PendingPermission(); p != nil && p.ToolCallID != "" {
		out[p.ToolCallID] = struct{}{}
	}
	return out
}

// findToolPairingBreak 正向扫描窗口，返回「工具调用配对断裂」时应截断到的窗口内索引。
//
// 配对规则（与 OpenAI 严格消息格式一致）：assistant 消息携带的 tool_calls 必须
// 由其**紧随其后**的 tool 消息逐一回应（按 tool_call_id 匹配），中间不得插入
// 其他角色消息。若在覆盖完所有 tool_call_id 之前遇到非 tool 消息，即为断裂点。
//
// 并发交错产生的坏序列典型形态是「tool 消息被后来的 assistant 隔开」，
// 如 asst(A) → asst(B) → tool(B) → tool(A)：此处 A 的 tool 虽存在但不紧邻，
// 仅做存在性检查会漏检，必须逐段验证紧邻覆盖。
//
// 断裂点需继续向前回退到最近的 user 消息之后，保证截断后的窗口自身完整
// 且以 user 结尾（消息格式合法）。
//
// 返回值是应截断到的窗口内索引（保留 window[:idx] 为完整合法对话），
// -1 表示整个窗口内配对完整、无需修复。
//
// suspended 是「结果尚未产生、将由后续轮次写入」的 tool_call ID 集合，包含
// 挂起提问与待授权工具两类。主会话以 ask_user_pending / permission_pending
// 终止时，这些 tool_call 的应答要等用户沟通后由下一轮写入，因此「窗口末尾
// 未应答的全是挂起项」属于合法等待态，不属于断裂——否则每轮入口都会把刚
// 提出的问题或刚发出的授权请求连同用户输入一起回滚。
func findToolPairingBreak(window []session.Message, suspended map[string]struct{}) int {
	for i := 0; i < len(window); {
		m := window[i]
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			// 该 assistant 的 tool_calls 必须由其后的 tool 消息紧邻且逐一覆盖。
			pending := make(map[string]struct{}, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				pending[tc.ID] = struct{}{}
			}
			j := i + 1
			for j < len(window) && window[j].Role == "tool" {
				if _, ok := pending[window[j].ToolCallID]; ok {
					delete(pending, window[j].ToolCallID)
				}
				j++
			}
			if len(pending) > 0 {
				// 挂起等待用户回应：缺的全是挂起项，且其后没有任何消息。
				if len(suspended) > 0 && j == len(window) {
					all := true
					for id := range pending {
						if _, ok := suspended[id]; !ok {
							all = false
							break
						}
					}
					if all {
						return -1
					}
				}
				// 断裂：tool_calls 未被紧邻的 tool 消息完全覆盖，回退到
				// 该 assistant 之前最近的 user 消息之后。
				cut := i
				for cut > 0 && !window[cut-1].IsRoundAnchor() {
					cut--
				}
				return cut
			}
			i = j
			continue
		}
		i++
	}
	return -1
}

// repairToolPairingBreak 检查会话当前窗口是否存在工具调用配对断裂。
// 若存在，将会话截断到断裂点之前（保留此前全部合法轮次），并把被截区间内的
// user 消息按原顺序重新追加——用户的提问/回答属于有效输入，不随坏轮次丢弃
// （连续 user 消息协议合法）；回补后末尾仍非 user 消息时追加一条说明消息，
// 让 LLM 明确重做一次。
//
// 返回值：
//   - repaired：是否执行了修复（窗口本身完整或修复失败返回 false）
//   - removedToolCallIDs：被截区间内全部 assistant tool_call ID——这些调用
//     已随坏轮次回滚，调用方据此判定挂起授权/挂起提问是否随之失效
func repairToolPairingBreak(ctx context.Context, s *session.Session, logger logging.Logger) (bool, map[string]struct{}) {
	window := s.Current()
	cut := findToolPairingBreak(window, suspendedToolCallIDs(s))
	if cut < 0 {
		return false, nil
	}

	// 记录被截区间内的 tool_call ID（供调用方失效挂起授权/提问）
	// 与 user 消息（修复后回补，保留原内容/图片/时间戳）。
	removedTCs := make(map[string]struct{})
	var keptUsers []session.Message
	for _, m := range window[cut:] {
		for _, tc := range m.ToolCalls {
			removedTCs[tc.ID] = struct{}{}
		}
		// 只回补真实用户提问：工具产出的视觉附件（user 角色但非提问）随坏轮次
		// 一起丢弃，不搬到窗口末尾冒充用户的下一句话。
		if m.IsRoundAnchor() {
			keptUsers = append(keptUsers, m)
		}
	}

	// 窗口内索引转换为完整消息数组的绝对索引（窗口 = messages[cursor:]）。
	keepCount := s.Cursor() + cut
	if err := s.Truncate(ctx, keepCount); err != nil {
		logger.Error("修复工具调用配对失败：截断会话出错", err,
			"session", s.ID(), "keepCount", keepCount)
		return false, nil
	}

	// 被截区间内的 user 消息按原顺序回补：不随坏轮次丢弃用户输入。
	for _, m := range keptUsers {
		if err := s.Append(ctx, session.Message{
			Role:      "user",
			Content:   m.Content,
			Images:    m.Images,
			Timestamp: m.Timestamp,
		}); err != nil {
			logger.Error("修复工具调用配对失败：回补用户消息出错", err, "session", s.ID())
			return true, removedTCs
		}
	}

	// 回补后若窗口为空或末尾不是 user 消息，追加说明消息：
	// 既保证序列以 user 结尾（消息格式合法），也让 LLM 明确需要重新规划。
	cur := s.Current()
	if len(cur) == 0 || cur[len(cur)-1].Role != "user" {
		if err := s.Append(ctx, session.Message{
			Role:      "user",
			Content:   toolPairingRepairNotice,
			Timestamp: time.Now().Unix(),
		}); err != nil {
			logger.Error("修复工具调用配对失败：追加说明消息出错", err,
				"session", s.ID())
			return true, removedTCs
		}
	}

	logger.Warn("已修复工具调用配对断裂：截断坏轮次并回补用户消息，LLM 将重做一次",
		"session", s.ID(), "keepCount", keepCount, "回补用户消息数", len(keptUsers))
	return true, removedTCs
}

// invalidatePendingsAfterRepair 在入口配对修复触发后，检查挂起授权与挂起提问
// 是否随坏轮次回滚而失效，并做对应闭环处置：
//   - 挂起授权的 ToolCallID 命中被截集合：取走 pending 并发射 PermissionDenied，
//     让前端撤下过期弹窗（避免魔法词命中已被回滚的旧请求）；
//   - 挂起提问的 ToolCallID 命中被截集合或缺失（无法定位对应调用）：丢弃并告警；
//     未受修复影响时放回，等待用户回答恢复。
func (rt *Runtime) invalidatePendingsAfterRepair(b *AskBuilder, removedTCs map[string]struct{}, emit func(events.ReactEventType, any)) {
	if p := b.session.PendingPermission(); p != nil {
		if _, hit := removedTCs[p.ToolCallID]; hit {
			b.session.TakePendingPermission()
			emit(events.PermissionDenied, "授权已过期：会话历史已回滚修复，原授权请求已被撤销")
			rt.logger.Warn("挂起授权随配对修复失效，已撤销",
				"session", b.session.ID(), "tool_call_id", p.ToolCallID)
		}
	}
	if p := b.session.TakePendingAskUser(); p != nil {
		if _, hit := removedTCs[p.ToolCallID]; hit || p.ToolCallID == "" {
			rt.logger.Warn("挂起提问随配对修复失效，已丢弃",
				"session", b.session.ID(), "question", p.Question)
		} else {
			b.session.SetPendingAskUser(*p)
		}
	}
}
