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
	"unicode/utf8"
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
// 由 Read 工具的 Execute 末尾调用；Edit/Write 写入成功后也会调用，以保持"已读"状态，
// 使同一文件的连续多次编辑无需反复 Read（借鉴 Claude FileEditTool 的 readFileState 设计）。
//
// 参数：
//   - path: 已解析的绝对路径
//   - readAt: 读取时间（通常为 time.Now()）
//   - content: 读取的文件内容（用于计算 hash）
//
// MtimeMs 优先取文件真实 mtime（通过 os.Stat），仅在 stat 失败时回退 readAt，
// 与 Claude 用 getFileModificationTime 记录时间戳的语义一致，避免毫秒级误差
// 导致后续 CheckStale 每次都触发 content hash 兜底计算。
func SetStale(path string, readAt time.Time, content []byte) {
	h := sha256.Sum256(content)
	mtimeMs := readAt.UnixMilli()
	if info, err := os.Stat(path); err == nil {
		mtimeMs = info.ModTime().UnixMilli()
	}
	staleStates.Store(path, &StaleState{
		Path:        path,
		ReadAt:      readAt,
		MtimeMs:     mtimeMs,
		ContentHash: fmt.Sprintf("%x", h[:]),
	})
}

// buildGuide 组装第一人称引导式错误消息（我做了什么 → 原因 → 下一步我应该）。
// 与 tools 包 BuildGuide 保持同一文案契约，但 filestate 是叶子包（tools 依赖它），
// 无法反向引用 tools，故在此自包含实现。
func buildGuide(action, cause, next string) string {
	return fmt.Sprintf("我%s。\n原因：%s。\n下一步我应该：%s", action, cause, next)
}

// errDetailMaxRunes 底层错误信息保留的最大长度。
// 与 tools 包 WithErrDetail 的截断策略一致，底层错误只是补充细节，
// 超长会浪费 Token，故截断。
const errDetailMaxRunes = 120

// withErrDetail 将底层错误信息截断后附加到原因描述末尾（err 为 nil 时原样返回 cause）。
func withErrDetail(cause string, err error) string {
	if err == nil {
		return cause
	}
	detail := err.Error()
	if n := utf8.RuneCountInString(detail); n > errDetailMaxRunes {
		runes := []rune(detail)
		detail = string(runes[:errDetailMaxRunes]) + "…"
	}
	return fmt.Sprintf("%s（底层错误：%s）", cause, detail)
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
			Code: ErrCodeActionable,
			Message: buildGuide(
				fmt.Sprintf("尝试编辑或写入文件 %q，但该文件尚未被读取过", path),
				"当前会话中没有 Read 工具读取该文件的记录，无法确认文件的最新内容",
				"先用 Read 工具读取该文件的最新内容，再执行编辑或写入",
			),
		}
	}
	ss := raw.(*StaleState)

	info, err := os.Stat(path)
	if err != nil {
		return &ToolError{
			Code: ErrCodeActionable,
			Message: buildGuide(
				fmt.Sprintf("尝试检查文件 %q 的最新状态", path),
				withErrDetail("无法获取该文件的状态", err),
				"先自查：该文件是否仍存在、路径是否正确、是否有访问权限？确认后用 Read 工具重新读取，再执行编辑或写入",
			),
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
			Code: ErrCodeActionable,
			Message: buildGuide(
				fmt.Sprintf("尝试读取文件 %q 的最新内容以确认它是否被外部修改", path),
				withErrDetail("该文件已被修改，且无法读取最新内容", readErr),
				"先检查文件是否仍可访问（可能已被移动/删除或权限变化），修正后用 Read 工具重新读取，再继续编辑或写入",
			),
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
		Code: ErrCodeActionable,
		Message: buildGuide(
			fmt.Sprintf("尝试编辑或写入文件 %q", path),
			"该文件自上次读取后已被外部修改，直接基于旧内容写入会覆盖外部改动",
			"先用 Read 工具重新读取该文件的最新内容，确认改动后再编辑或写入",
		),
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
