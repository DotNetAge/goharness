package session

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
)

// pendingAskFileName 是存储在会话目录中的挂起提问 JSON 文件名。
// 持久化先例与待处理授权（session-permission.json）一致：主会话的 AskUser
// 提问以 ask_user_pending 终止本轮后，daemon 每次 user.message 都会重新
// Load 全新 Session 实例，若只在内存保存，用户的回答到达时 pending 必然
// 丢失，提问-回答的协议补全（tool 消息）就会退化为断裂窗口。
const pendingAskFileName = "session-ask-user.json"

// pendingAskPath 返回挂起提问文件的绝对路径。
// 当会话没有持久化目录（无 store）时返回空字符串。
func (s *Session) pendingAskPath() string {
	dir := s.SessionDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, pendingAskFileName)
}

// loadPendingAsk 从磁盘恢复挂起提问到内存。
// 文件不存在或解析失败时静默返回（无 pending 等价于无挂起提问）。
func (s *Session) loadPendingAsk() {
	pp := s.pendingAskPath()
	if pp == "" {
		return
	}
	data, err := os.ReadFile(pp)
	if err != nil {
		return
	}
	var p PendingAskUser
	if json.Unmarshal(data, &p) != nil {
		// 文件损坏：丢弃，等价于无挂起提问。
		return
	}
	s.pendingMu.Lock()
	s.pendingAskUser = &p
	s.pendingMu.Unlock()
}

// persistPendingAsk 将当前挂起提问落盘；p 为 nil 时删除文件。
// 持久化失败仅记录日志（尽力而为），不阻断主流程。
func (s *Session) persistPendingAsk(p *PendingAskUser) {
	pp := s.pendingAskPath()
	if pp == "" {
		return
	}
	if p == nil {
		if err := os.Remove(pp); err != nil && !os.IsNotExist(err) {
			s.log.Error("删除挂起提问文件失败", err, "path", pp)
		}
		return
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		s.log.Error("序列化挂起提问失败", err)
		return
	}
	if err := os.WriteFile(pp, data, 0644); err != nil {
		s.log.Error("写入挂起提问文件失败", err, "path", pp)
	}
}

// PendingAskUser 捕获主会话以 ask_user_pending 终止时未回答的提问。
// 主会话与子会话的恢复路径不同：子会话由 waitForAskUserDecision 阻塞等待
// 并当场补全 tool 消息；主会话挂起即终止，用户的下一条消息视为回答
// （ask_user_pending 的产品语义），下一轮 Run 入口据此补全协议。
type PendingAskUser struct {
	// ToolCallID 与产生此次提问的助手消息上的 ToolCall.ID 匹配。
	// 用户的回答以此 ID 作为 AskUser 工具结果消息追加到会话，满足
	// OpenAI 严格契约：每个 tool_call 都必须有对应的 tool 消息。
	ToolCallID string `json:"tool_call_id"`

	// Question 是发起提问时的问题文本（排障与前端提示用）。
	Question string `json:"question,omitempty"`
}

// SetPendingAskUser 记录等待用户回答的提问，并持久化到会话目录。
// 与 SetPendingPermission 一致先触发懒加载（确保磁盘状态先行恢复，避免
// 覆盖尚未加载的挂起提问）。
func (s *Session) SetPendingAskUser(p PendingAskUser) {
	s.ensureLoaded(context.Background())
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	s.pendingAskUser = &p
	s.persistPendingAsk(&p)
}

// TakePendingAskUser 原子地读取并清除挂起的提问。
func (s *Session) TakePendingAskUser() *PendingAskUser {
	s.ensureLoaded(context.Background())
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	p := s.pendingAskUser
	s.pendingAskUser = nil
	if s.pendingAskPath() != "" {
		s.persistPendingAsk(nil)
	}
	return p
}

// HasPendingAskUser 报告会话当前是否存在等待回答的提问。
func (s *Session) HasPendingAskUser() bool {
	s.ensureLoaded(context.Background())
	s.pendingMu.Lock()
	defer s.pendingMu.Unlock()
	return s.pendingAskUser != nil
}
