package session

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// pendingPermissionFileName 是存储在会话目录中的待处理授权 JSON 文件名。
// 沿用 session-wl.json（白名单）的持久化先例：待处理授权是会话级共享状态，
// 必须跨 Ask() 调用（甚至跨进程重启）存活——daemon 每次 user.message 都会
// 重新 Load 全新 Session 实例，若只在内存保存，魔法词到达时 pending 必然丢失，
// 主会话授权链就会断裂。
const pendingPermissionFileName = "session-permission.json"

// pendingPermissionPath 返回待处理授权文件的绝对路径。
// 当会话没有持久化目录（无 store）时返回空字符串。
func (s *Session) pendingPermissionPath() string {
	dir := s.SessionDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, pendingPermissionFileName)
}

// loadPendingPermission 从磁盘恢复待处理授权到内存。
// 文件不存在或解析失败时静默返回（无 pending 等价于无授权请求）。
func (s *Session) loadPendingPermission() {
	pp := s.pendingPermissionPath()
	if pp == "" {
		return
	}
	data, err := os.ReadFile(pp)
	if err != nil {
		return
	}
	var p PendingPermission
	if json.Unmarshal(data, &p) != nil {
		// 文件损坏：丢弃，等价于无待处理授权。
		return
	}
	s.pendingMu.Lock()
	s.pendingPermission = &p
	s.pendingMu.Unlock()
}

// persistPendingPermission 将当前待处理授权落盘；p 为 nil 时删除文件。
// 持久化失败仅记录日志（尽力而为），不阻断主流程——
// 无存储目录的会话（如纯内存测试）允许仅内存保存。
func (s *Session) persistPendingPermission(p *PendingPermission) {
	pp := s.pendingPermissionPath()
	if pp == "" {
		return
	}
	if p == nil {
		if err := os.Remove(pp); err != nil && !os.IsNotExist(err) {
			s.log.Error("删除待处理授权文件失败", err, "path", pp)
		}
		return
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		s.log.Error("序列化待处理授权失败", err)
		return
	}
	if err := os.WriteFile(pp, data, 0644); err != nil {
		s.log.Error("写入待处理授权文件失败", err, "path", pp)
	}
}

// PendingPermission 捕获正在等待用户决策的工具调用。
// 它持有运行时实际执行工具（Allow）或合成"权限拒绝"工具
// 结果（Deny）所需的全部信息，而 LLM 永远不会看到 "ask"
// 这个中间状态。
type PendingPermission struct {
	// ToolName 是已注册工具的名称（例如 "Bash"、"Write"）。
	ToolName string `json:"tool_name"`

	// ToolCallID 与产生此次调用的助手消息上的 ToolCall.ID 匹配。
	// 合成的"权限拒绝"结果（或实际执行工具的结果）会以此 ID
	// 追加到会话中，以满足 OpenAI 的严格契约：每个 tool_call
	// 都必须有对应的 tool 消息。
	ToolCallID string `json:"tool_call_id"`

	// Arguments 是最初传给工具的参数 map。当用户允许时，
	// 运行时使用这些精确参数重新调用工具 —— 不会重新推导。
	Arguments map[string]any `json:"arguments,omitempty"`

	// Reason 是已经在 UI 中展示的可读说明
	// （例如 "command contains 'rm -rf /'"）。它会被复用到
	// "权限拒绝"工具结果中，让 LLM 看到调用被拒的原因。
	Reason string `json:"reason,omitempty"`

	// SecurityLevel 保留自工具的 ToolInfo，以便 UI
	//（以及未来的审计日志）能渲染正确的严重级别徽章。
	SecurityLevel string `json:"security_level,omitempty"`
}
