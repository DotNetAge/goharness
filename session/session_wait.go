package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// waitMarkerFileName 是存储在会话目录中的「等待用户输入」标记文件名。
//
// 存在的必要性（收割 blocked 判定的第三路信号）：子会话的提问/授权冒泡
// 路径进入 waitForAskUserDecision / waitForPermissionDecision 阻塞等待后，
// 未决提问 sidecar（session-ask-user.json）对子会话从不落盘，未决授权
// sidecar（session-permission.json）在等待开始时已被 TakePendingPermission
// 取走——等待窗口内磁盘上没有任何挂起痕迹，外部轮询方（如宿主的收割
// 轮询器）无法区分「在等待用户」与「在执行」。本标记在等待入口写、出口删，
// 补齐该窗口；daemon 重启后等待必然消亡，宿主 daemon 启动时统一清扫
// 陈旧标记（清扫职责在宿主侧：本包的会话加载不清理——加载既覆盖不到
// 从未被打开的残留会话，又会在活跃等待期间被常规 Load 误删生效标记）。
const waitMarkerFileName = "session-wait.json"

// SessionWaitMarker 是等待标记的落盘结构（仅排障用，判定方只看文件存在性）。
type SessionWaitMarker struct {
	Kind  string `json:"kind"` // "ask" 或 "permission"
	Since int64  `json:"since"`
}

func (s *Session) waitMarkerPath() string {
	dir := s.SessionDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, waitMarkerFileName)
}

// MarkWaitingUser 写入等待标记（等待入口调用）。落盘失败仅记日志
//（尽力而为，不阻断挂起等待主流程）。
func (s *Session) MarkWaitingUser(kind string) {
	pp := s.waitMarkerPath()
	if pp == "" {
		return
	}
	data, err := json.Marshal(SessionWaitMarker{Kind: kind, Since: time.Now().Unix()})
	if err != nil {
		s.log.Error("序列化等待标记失败", err)
		return
	}
	if err := os.WriteFile(pp, data, 0644); err != nil {
		s.log.Error("写入等待标记失败", err, "path", pp)
	}
}

// ClearWaitingUser 删除等待标记（等待出口调用，所有退出路径都应经过 defer
// 保证执行）。文件不存在视为成功。
func (s *Session) ClearWaitingUser() {
	pp := s.waitMarkerPath()
	if pp == "" {
		return
	}
	if err := os.Remove(pp); err != nil && !os.IsNotExist(err) {
		s.log.Error("删除等待标记失败", err, "path", pp)
	}
}
