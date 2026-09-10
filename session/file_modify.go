package session

import (
	"fmt"
	"hash/fnv"
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
//   - 如果文件已在 modifyFiles 中，跳过重复备份，但仍触发事件（见下）
//   - 如果文件存在且未被追踪过，将其备份到 Session 的 Backup 目录
//   - 将文件路径加入 modifyFiles 数组
//   - 发出事件（首次追踪与再次修改都触发；新文件也会触发，此时 backupPath 为空）
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

	// 已在追踪列表中：不重复备份，但仍触发事件。
	//
	// 再次修改意味着文件内容相对「首次追踪时」已发生变化，而外部（如 daemon
	// 前端桥）持有的 diff 是旧快照；必须通知外部重新拉取，否则前端展示的
	// diff 与当前文件内容失配（历史上导致 FileReviewBar 打不开 diff 视图）。
	if s.containsModifyFile(cleanPath) {
		if s.fileModifyHandler != nil {
			s.fileModifyHandler(FileModifyEvent{
				FilePath:   cleanPath,
				BackupPath: s.backupPathFor(cleanPath),
				Action:     "tracked",
			})
		}
		return nil
	}

	// 文件不存在则无需备份（新文件），但仍需追踪
	var backupPath string
	if fileExists(cleanPath) {
		bp, err := s.backupFile(cleanPath)
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
		// 读取侧回退旧键：升级前会话的存量备份是旧命名，读不到新键时须能命中
		backupPath := s.backupPathForRead(fp)
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
		// 读取侧回退旧键：升级前会话的存量备份是旧命名，读不到新键时须能命中，
		// 否则回滚会静默移除追踪而不还原内容
		backupPath := s.backupPathForRead(fp)

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
func (s *Session) backupFile(srcPath string) (string, error) {
	data, err := os.ReadFile(srcPath)
	if err != nil {
		return "", fmt.Errorf("read source: %w", err)
	}

	backupPath := s.backupPathFor(srcPath)

	if err := os.WriteFile(backupPath, data, 0644); err != nil {
		return "", fmt.Errorf("write backup: %w", err)
	}

	return backupPath, nil
}

// backupKeyFor 计算源文件的备份键：原始文件名 + 绝对路径 FNV 散列。
//
// 不能只用文件名（filepath.Base）做键：不同目录下的同名文件（如 pkg/a/util.go
// 与 pkg/b/util.go）会共用同一个 .bak，后追踪者覆盖先追踪者的备份，导致
// diff 展示错乱、回滚把 A 文件内容写进 B 文件。附加路径散列即可唯一化，
// 同时保留文件名前缀便于人工排查备份目录。
func backupKeyFor(absPath string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(absPath))
	return fmt.Sprintf("%s-%x.bak", filepath.Base(absPath), h.Sum64())
}

// legacyBackupKeyFor 返回旧版备份键（原始文件名.bak）。
// 仅用于读取回退：键规则引入路径散列之前创建的存量备份以此命名。
func legacyBackupKeyFor(absPath string) string {
	return filepath.Base(absPath) + ".bak"
}

// backupPathFor 返回源文件在当前会话备份目录中的备份文件完整路径。
// 备份/确认/回滚/外部读取 diff 必须统一经由此方法取路径，禁止各自拼键。
func (s *Session) backupPathFor(fp string) string {
	return filepath.Join(s.resolveBackupDir(), backupKeyFor(fp))
}

// backupPathForRead 返回读取备份时实际可用的路径：新键存在优先；否则回退
// 旧键（升级前创建的会话其备份文件仍是旧命名，且已追踪文件不会重新备份，
// 新代码下永远等不到新键备份）。读取侧统一走此方法，避免升级窗口内活跃
// 会话的回滚静默失效、diff 被误判为全量新增。两键都不存在时返回新键路径
// （与旧键路径的差别仅体现为调用方的 fileExists 检查结果）。
func (s *Session) backupPathForRead(fp string) string {
	p := s.backupPathFor(fp)
	if fileExists(p) {
		return p
	}
	legacy := filepath.Join(s.resolveBackupDir(), legacyBackupKeyFor(fp))
	if fileExists(legacy) {
		return legacy
	}
	return p
}

// BackupPathFor 导出：返回源文件在当前会话备份目录中的备份文件完整路径。
// 供上层服务（如 mindx daemon 计算 modify_files diff）复用同一命名规则，
// 避免两处独立实现键规则造成漂移。
func (s *Session) BackupPathFor(fp string) string {
	return s.backupPathFor(cleanFilePath(fp))
}

// BackupPathForRead 导出：读取侧备份路径（新键优先，旧键回退）。
// 供上层服务读取备份基线使用；写入侧（备份创建）仍必须用 BackupPathFor。
func (s *Session) BackupPathForRead(fp string) string {
	return s.backupPathForRead(cleanFilePath(fp))
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
