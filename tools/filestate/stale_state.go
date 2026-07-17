// Package filestate 提供文件状态管理，用于跨工具的文件读取时间跟踪。
//
// 用途：
//   - Read 在成功读取后调用 SetStale 记录读取时间
//   - Edit/Write 在写入前调用 CheckStale 检测文件是否过期
//
// 约定见 FILE_TOOLS_ARCHITECTURE.md 公约 1。
package filestate

import (
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"time"
)

// StaleState 记录一次文件读取的状态。
// 用于 Edit/Write 在写入前检测文件是否已被外部修改。
type StaleState struct {
	Path        string    // 已解析的绝对路径
	ReadAt      time.Time // 读取时间（用于 mtime 比对的基准）
	MtimeMs     int64     // 读取时的文件 mtime（毫秒）
	ContentHash string    // 读取内容的 SHA256（用于 mtime 误报时的 content 双重校验）
}

var staleStates sync.Map

// SetStale 在文件成功读取后记录状态。
// 由 Read 工具的 Execute 末尾调用。
//
// 参数：
//   - path: 已解析的绝对路径
//   - readAt: 读取时间（通常为 time.Now()）
//   - content: 读取的文件内容（用于计算 hash）
func SetStale(path string, readAt time.Time, content []byte) {
	h := sha256.Sum256(content)
	staleStates.Store(path, &StaleState{
		Path:        path,
		ReadAt:      readAt,
		MtimeMs:     readAt.UnixMilli(),
		ContentHash: fmt.Sprintf("%x", h[:]),
	})
}

// CheckStale 检测文件是否在读取后发生过修改。
//
// 返回值：
//   - nil: 文件新鲜，可以写入
//   - ErrNotRead: 该路径从未被读取过
//   - ErrStale: 文件已被外部修改
func CheckStale(path string) error {
	raw, ok := staleStates.Load(path)
	if !ok {
		return &ToolError{
			Code:    ErrCodeActionable,
			Message: fmt.Sprintf("文件 %s 未读取。请先使用 Read 工具读取文件后再编辑或写入。", path),
		}
	}
	ss := raw.(*StaleState)

	info, err := os.Stat(path)
	if err != nil {
		return &ToolError{
			Code:    ErrCodeActionable,
			Message: fmt.Sprintf("无法获取文件 %s 的状态：%v。请确认文件存在后重试。", path, err),
		}
	}

	// mtime 一致 → 文件未修改
	if info.ModTime().UnixMilli() == ss.MtimeMs {
		return nil
	}

	// mtime 变更 → 做 content hash 双重校验
	// 解决 Windows 云同步/杀毒软件误改 mtime 但不改内容的问题
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		return &ToolError{
			Code:    ErrCodeActionable,
			Message: fmt.Sprintf("文件 %s 已被修改，且无法读取最新内容（%v）。请先使用 Read 工具重新读取。", path, readErr),
		}
	}
	h := sha256.Sum256(content)
	newHash := fmt.Sprintf("%x", h[:])
	if newHash == ss.ContentHash {
		// mtime 变了但内容没变 → 更新 mtime 记录
		ss.MtimeMs = info.ModTime().UnixMilli()
		return nil
	}

	return &ToolError{
		Code:    ErrCodeActionable,
		Message: fmt.Sprintf("文件 %s 自上次读取后已被外部修改。请在编辑前使用 Read 工具重新读取文件。", path),
		Internal: fmt.Sprintf("mtime changed from %d to %d, content hash changed from %s to %s",
			ss.MtimeMs, info.ModTime().UnixMilli(), ss.ContentHash, newHash),
	}
}

// DeleteStale 在写入成功后清除指定路径的 StaleState。
// 由 Edit/Write 的 Execute 末尾调用，确保后续操作是"未读"状态。
func DeleteStale(path string) {
	staleStates.Delete(path)
}

// TTLBefore 将 StaleState 的生命周期限制为 TTL 秒。
// 超过 TTL 的条目视为过期（返回 ErrNotRead），避免内存泄漏。
// 默认调用间隔：每次 CheckStale 时惰性检查。
var staleStateTTL time.Duration

// SetTTL 设置 StaleState 的过期时间。
// 0 表示永不过期（默认）。
func SetTTL(d time.Duration) {
	staleStateTTL = d
}

// IsExpired 检查 StaleState 是否已超过 TTL。
// 用于清理过期状态的惰性策略。
func (s *StaleState) IsExpired() bool {
	if staleStateTTL <= 0 {
		return false
	}
	return time.Since(s.ReadAt) > staleStateTTL
}
