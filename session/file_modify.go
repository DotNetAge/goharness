package session

import (
	"fmt"
	"os"
	"path/filepath"
)

// FileModifyEvent 携带文件修改追踪事件的数据，用于通知外部监听者。
type FileModifyEvent struct {
	// FilePath 被修改的文件绝对路径（单文件操作时使用）
	FilePath string

	// BackupPath 备份文件存放路径（追踪时使用）
	BackupPath string

	// FilePaths 被操作的文件路径列表（批量确认/回滚时使用）
	FilePaths []string

	// Action 事件类型: "tracked" | "confirmed" | "rolled_back"
	Action string
}

// FileModifyHandler 是文件修改事件的回调函数类型。
type FileModifyHandler func(FileModifyEvent)

// ── 文件修改追踪：Session 扩展方法 ──────────────────────────────────────

// TrackModify 追踪一个文件的修改。
//
// 当 Write、FileEdit 等工具即将修改文件时调用此方法：
//   - 如果文件已在 modifyFiles 中，跳过（不重复备份）
//   - 如果文件存在且未被追踪过，将其备份到 Session 的 Backup 目录
//   - 将文件路径加入 modifyFiles 数组
//   - 发出事件（首次追踪时；新文件也会触发，此时 backupPath 为空）
//
// 参数：
//   - filePath: 即将被修改的文件的绝对路径
//
// 返回：
//   - error: 备份失败时返回错误（文件不会被加入 modifyFiles）
func (s *Session) TrackModify(filePath string) error {
	cleanPath := cleanFilePath(filePath)

	s.mu.Lock()
	defer s.mu.Unlock()

	// 已在追踪列表中，跳过
	if s.containsModifyFile(cleanPath) {
		return nil
	}

	// 文件不存在则无需备份（新文件），但仍需追踪
	backupDir := s.resolveBackupDir()

	var backupPath string
	if fileExists(cleanPath) {
		bp, err := s.backupFile(cleanPath, backupDir)
		if err != nil {
			return fmt.Errorf("track modify: backup %q failed: %w", cleanPath, err)
		}
		backupPath = bp
	}

	// 加入追踪列表
	s.modifyFiles = append(s.modifyFiles, cleanPath)
	s.persistModifyFilesLocked()

	// 触发事件。
	//
	// 注意：新文件（fileExists == false）的 backupPath 为空字符串，
	// 但仍需触发事件，让前端能够显示「新增文件」的 DiffView。
	// containsModifyFile 已在上面对重复追踪做了去重，
	// 因此同一文件被多次修改时，handler 只会在首次追踪时触发一次。
	if s.fileModifyHandler != nil {
		s.fileModifyHandler(FileModifyEvent{
			FilePath:   cleanPath,
			BackupPath: backupPath,
			Action:     "tracked",
		})
	}

	return nil
}

// ConfirmModify 确认文件修改：删除备份文件并从 ModifyFiles 中移除指定文件。
//
// 可同时对多个文件操作。如果 files 为空，则确认所有已追踪文件。
//
// 参数：
//   - files: 要确认的文件路径（绝对路径）。为空则确认全部。
//
// 返回：
//   - []string: 实际被确认的文件路径列表
//   - error: 删除备份失败时的错误
func (s *Session) ConfirmModify(files ...string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	targets := s.resolveTargets(files...)
	confirmed := make([]string, 0, len(targets))

	for _, fp := range targets {
		// 删除备份文件
		backupDir := s.resolveBackupDir()
		backupPath := filepath.Join(backupDir, filepath.Base(fp)+".bak")
		if fileExists(backupPath) {
			if err := os.Remove(backupPath); err != nil {
				return confirmed, fmt.Errorf("confirm modify: remove backup %q failed: %w", backupPath, err)
			}
		}

		// 从追踪列表移除
		s.removeModifyFileLocked(fp)
		confirmed = append(confirmed, fp)
	}

	s.persistModifyFilesLocked()

	// 触发事件
	if s.fileModifyHandler != nil && len(confirmed) > 0 {
		s.fileModifyHandler(FileModifyEvent{
			FilePaths: confirmed,
			Action:    "confirmed",
		})
	}

	return confirmed, nil
}

// Rollback 回滚文件修改：从备份恢复文件到原位置，并从 ModifyFiles 中移除。
//
// 可同时对多个文件操作。如果 files 为空，则回滚所有已追踪文件。
//
// 参数：
//   - files: 要回滚的文件路径（绝对路径）。为空则回滚全部。
//
// 返回：
//   - []string: 实际被回滚的文件路径列表
//   - error: 恢复失败时的错误
func (s *Session) Rollback(files ...string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	targets := s.resolveTargets(files...)
	rolledBack := make([]string, 0, len(targets))

	for _, fp := range targets {
		backupDir := s.resolveBackupDir()
		backupPath := filepath.Join(backupDir, filepath.Base(fp)+".bak")

		if !fileExists(backupPath) {
			// 无备份文件，直接移除追踪即可
			s.removeModifyFileLocked(fp)
			rolledBack = append(rolledBack, fp)
			continue
		}

		// 从备份恢复
		data, err := os.ReadFile(backupPath)
		if err != nil {
			return rolledBack, fmt.Errorf("rollback: read backup %q failed: %w", backupPath, err)
		}

		// 确保目标目录存在
		dir := filepath.Dir(fp)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return rolledBack, fmt.Errorf("rollback: mkdir %q failed: %w", dir, err)
		}

		if err := os.WriteFile(fp, data, 0644); err != nil {
			return rolledBack, fmt.Errorf("rollback: restore %q failed: %w", fp, err)
		}

		// 删除备份文件
		os.Remove(backupPath)

		// 从追踪列表移除
		s.removeModifyFileLocked(fp)
		rolledBack = append(rolledBack, fp)
	}

	s.persistModifyFilesLocked()

	// 触发事件
	if s.fileModifyHandler != nil && len(rolledBack) > 0 {
		s.fileModifyHandler(FileModifyEvent{
			FilePaths: rolledBack,
			Action:    "rolled_back",
		})
	}

	return rolledBack, nil
}

// GetModifyFiles 返回当前被追踪的修改文件列表（副本）。
func (s *Session) GetModifyFiles() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.modifyFiles))
	copy(out, s.modifyFiles)
	return out
}

// SetFileModifyHandler 设置文件修改事件回调。
func (s *Session) SetFileModifyHandler(handler FileModifyHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fileModifyHandler = handler
}

// HasModifyFiles 检查是否有被追踪的修改文件。
func (s *Session) HasModifyFiles() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.modifyFiles) > 0
}

// ── 内部方法 ─────────────────────────────────────────────────────────────

// containsModifyFile 检查文件是否已在追踪列表中（需要持有写锁）。
func (s *Session) containsModifyFile(path string) bool {
	for _, f := range s.modifyFiles {
		if f == path {
			return true
		}
	}
	return false
}

// removeModifyFileLocked 从追踪列表中移除文件（需要持有写锁）。
func (s *Session) removeModifyFileLocked(path string) {
	filtered := s.modifyFiles[:0]
	for _, f := range s.modifyFiles {
		if f != path {
			filtered = append(filtered, f)
		}
	}
	s.modifyFiles = filtered
}

// resolveTargets 解析要操作的文件列表。如果未指定，返回全部追踪文件。
func (s *Session) resolveTargets(files ...string) []string {
	if len(files) == 0 {
		out := make([]string, len(s.modifyFiles))
		copy(out, s.modifyFiles)
		return out
	}
	targets := make([]string, 0, len(files))
	for _, f := range files {
		cp := cleanFilePath(f)
		if s.containsModifyFile(cp) {
			targets = append(targets, cp)
		}
	}
	return targets
}

// resolveBackupDir 解析并确保备份目录存在。
func (s *Session) resolveBackupDir() string {
	sessionDir := s.SessionDir()
	if sessionDir == "" {
		// 无持久化存储时使用临时目录
		sessionDir = filepath.Join(os.TempDir(), "goharness-backups", s.id)
	}
	backupDir := filepath.Join(sessionDir, "backup")
	os.MkdirAll(backupDir, 0755)
	return backupDir
}

// backupFile 将源文件复制到备份目录。返回备份文件路径。
func (s *Session) backupFile(srcPath, backupDir string) (string, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("read source: %w", err)
	}

	fileName := filepath.Base(srcPath)
	// 使用固定命名：原始文件名.bak
	backupPath := filepath.Join(backupDir, fileName+".bak")

	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}

	return backupPath, nil
}

// persistModifyFilesLocked 将 modifyFiles 持久化到 store（需要持有写锁）。
func (s *Session) persistModifyFilesLocked() {
	if s.store == nil {
		return
	}
	_ = s.store.SaveModifyFiles(s.id, s.modifyFiles)
}

// loadModifyFiles 从 store 加载 modifyFiles（用于 lazy-load 阶段恢复）。
func (s *Session) loadModifyFiles() {
	if s.store == nil {
		return
	}
	files, err := s.store.GetModifyFiles(s.id)
	if err != nil || files == nil {
		return
	}
	s.mu.Lock()
	s.modifyFiles = files
	s.mu.Unlock()
}

// ── 工具函数 ─────────────────────────────────────────────────────────────

// cleanFilePath 清理文件路径，去除冗余成分并转为绝对路径。
func cleanFilePath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return filepath.Clean(abs)
}

// fileExists 检查文件是否存在且为常规文件。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
