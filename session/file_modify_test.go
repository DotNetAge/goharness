package session

import (
	"os"
	"path/filepath"
	"testing"
)

// ── Test helpers ───────────────────────────────────────────────────────────

func newTestSessionWithModify() *Session {
	return &Session{
		id:           "test-session-modify",
		agentName:    "test-agent",
		messages:     make([]Message, 0),
		store:        newMockStore(),
		mem:          newInMemoryMemory(),
		modifyFiles:  make([]string, 0),
	}
}

func createTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("创建临时文件失败: %v", err)
	}
	return path
}

// ── TrackModify tests ─────────────────────────────────────────────────────

func TestTrackModify_ExistingFile(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := createTempFile(t, tmpDir, "test.txt", "original content")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir

	err := s.TrackModify(filePath)
	if err != nil {
		t.Fatalf("TrackModify 不应返回错误: %v", err)
	}

	files := s.GetModifyFiles()
	if len(files) != 1 {
		t.Fatalf("期望 1 个追踪文件，得到 %d", len(files))
	}
	if files[0] != filePath {
		t.Errorf("追踪文件路径不匹配: got %q, want %q", files[0], filePath)
	}

	// 验证备份文件存在
	backupPath := s.BackupPathFor(filePath)
	if !fileExists(backupPath) {
		t.Error("备份文件应存在")
	}
	data, _ := os.ReadFile(backupPath)
	if string(data) != "original content" {
		t.Errorf("备份内容不匹配: got %q, want %q", string(data), "original content")
	}
}

func TestTrackModify_NewFile_NoBackup(t *testing.T) {
	tmpDir := t.TempDir()
	newFilePath := filepath.Join(tmpDir, "new_file.txt")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir

	err := s.TrackModify(newFilePath)
	if err != nil {
		t.Fatalf("TrackModify 对新文件不应返回错误: %v", err)
	}

	files := s.GetModifyFiles()
	if len(files) != 1 {
		t.Fatalf("新文件也应被追踪: got %d files", len(files))
	}

	// 新文件不应有备份
	backupPath := s.BackupPathFor(newFilePath)
	if fileExists(backupPath) {
		t.Error("新文件不应创建备份")
	}
}

func TestTrackModify_Duplicate_SkipBackup(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := createTempFile(t, tmpDir, "dup.txt", "content")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir

	// 第一次追踪
	if err := s.TrackModify(filePath); err != nil {
		t.Fatal(err)
	}

	// 修改原文件内容（模拟工具已执行）
	os.WriteFile(filePath, []byte("modified content"), 0644)

	// 第二次追踪同一文件 - 应跳过
	if err := s.TrackModify(filePath); err != nil {
		t.Fatal(err)
	}

	files := s.GetModifyFiles()
	if len(files) != 1 {
		t.Errorf("重复追踪不应增加条目: got %d", len(files))
	}

	// 备份应仍为原始内容
	backupPath := s.BackupPathFor(filePath)
	data, _ := os.ReadFile(backupPath)
	if string(data) != "content" {
		t.Errorf("备份应保持首次内容: got %q, want %q", string(data), "content")
	}
}

// TestTrackModify_SameBaseName_NoCollision 验证不同目录下的同名文件
// 各自拥有独立备份：后追踪者不得覆盖先追踪者的备份（回归防护——
// 旧实现用 filepath.Base 做备份键，会互相覆盖导致 diff 错乱、回滚串文件）。
func TestTrackModify_SameBaseName_NoCollision(t *testing.T) {
	tmpDir := t.TempDir()
	dirA := filepath.Join(tmpDir, "pkg_a")
	dirB := filepath.Join(tmpDir, "pkg_b")
	os.MkdirAll(dirA, 0755)
	os.MkdirAll(dirB, 0755)
	fA := createTempFile(t, dirA, "util.go", "package a")
	fB := createTempFile(t, dirB, "util.go", "package b")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir

	if err := s.TrackModify(fA); err != nil {
		t.Fatal(err)
	}
	if err := s.TrackModify(fB); err != nil {
		t.Fatal(err)
	}

	// 两个备份必须并存且内容各自正确
	backupA := s.BackupPathFor(fA)
	backupB := s.BackupPathFor(fB)
	if backupA == backupB {
		t.Fatalf("同名文件的备份路径不应相同: %q", backupA)
	}
	if data, err := os.ReadFile(backupA); err != nil || string(data) != "package a" {
		t.Errorf("A 文件备份缺失或内容被覆盖: err=%v, content=%q", err, string(data))
	}
	if data, err := os.ReadFile(backupB); err != nil || string(data) != "package b" {
		t.Errorf("B 文件备份缺失或内容被覆盖: err=%v, content=%q", err, string(data))
	}
}

// TestBackupPathForRead_LegacyFallback 验证读取侧旧键回退：
// 备份键引入路径散列之前创建的会话，其存量备份为旧命名（basename.bak），
// 且已追踪文件在新代码下不会重新生成新键备份——读取（回滚/确认/diff 基线）
// 必须能回退命中旧键，否则升级窗口内活跃会话会回滚静默失效、diff 被误判为
// 全量新增。新键存在时仍优先新键。
func TestBackupPathForRead_LegacyFallback(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := createTempFile(t, tmpDir, "legacy.txt", "package b")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir

	// 模拟升级前会话状态：旧键备份已在磁盘、追踪列表已从持久化恢复
	if err := os.WriteFile(filepath.Join(s.resolveBackupDir(), "legacy.txt.bak"), []byte("package a"), 0644); err != nil {
		t.Fatal(err)
	}
	s.modifyFiles = append(s.modifyFiles, filePath)

	// 读取侧应回退命中旧键
	legacyPath := filepath.Join(s.resolveBackupDir(), "legacy.txt.bak")
	if got := s.BackupPathForRead(filePath); got != legacyPath {
		t.Fatalf("新键缺失时应回退旧键: got %q, want %q", got, legacyPath)
	}

	// 回滚应命中旧键并还原内容（回归防护：旧代码下此处静默移除追踪不还原）
	rolled, err := s.Rollback(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(rolled) != 1 {
		t.Fatalf("应回滚 1 个文件: %v", rolled)
	}
	if data, _ := os.ReadFile(filePath); string(data) != "package a" {
		t.Errorf("内容应被还原为备份内容: got %q", string(data))
	}
	if fileExists(legacyPath) {
		t.Error("回滚后旧键备份应被删除")
	}
}

// TestBackupPathForRead_PreferNewKey 验证新键存在时读取侧优先新键，
// 旧键回退不干扰正常路径。
func TestBackupPathForRead_PreferNewKey(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := createTempFile(t, tmpDir, "normal.txt", "new content")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir

	if err := s.TrackModify(filePath); err != nil {
		t.Fatal(err)
	}

	// 新键备份已存在：读取侧直接返回新键
	if got := s.BackupPathForRead(filePath); got != s.BackupPathFor(filePath) {
		t.Fatalf("新键存在时应返回新键: got %q, want %q", got, s.BackupPathFor(filePath))
	}

	// 两键皆不存在（新文件无备份）：返回新键路径，由调用方 fileExists 判定
	fresh := createTempFile(t, tmpDir, "fresh.txt", "data")
	if got := s.BackupPathForRead(fresh); got != s.BackupPathFor(fresh) {
		t.Fatalf("两键皆无时应返回新键路径: got %q, want %q", got, s.BackupPathFor(fresh))
	}
}

func TestTrackModify_EventFired(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := createTempFile(t, tmpDir, "event.txt", "data")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir

	var receivedEvent FileModifyEvent
	s.SetFileModifyHandler(func(ev FileModifyEvent) {
		receivedEvent = ev
	})

	err := s.TrackModify(filePath)
	if err != nil {
		t.Fatal(err)
	}

	if receivedEvent.Action != "tracked" {
		t.Errorf("事件 action 应为 tracked, got %q", receivedEvent.Action)
	}
	if receivedEvent.FilePath != filePath {
		t.Errorf("事件 FilePath 不匹配: got %q, want %q", receivedEvent.FilePath, filePath)
	}
	if receivedEvent.BackupPath == "" {
		t.Error("事件 BackupPath 不应为空")
	}
}

// TestTrackModify_EventFiredForNewFile 验证新文件（无备份）也会触发事件，
// 以便前端能够显示「新增文件」的 DiffView；此时 BackupPath 应为空。
func TestTrackModify_EventFiredForNewFile(t *testing.T) {
	tmpDir := t.TempDir()
	newFilePath := filepath.Join(tmpDir, "new_file.txt")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir

	eventFired := false
	var receivedEvent FileModifyEvent
	s.SetFileModifyHandler(func(ev FileModifyEvent) {
		eventFired = true
		receivedEvent = ev
	})

	if err := s.TrackModify(newFilePath); err != nil {
		t.Fatal(err)
	}

	if !eventFired {
		t.Error("新文件（无备份）也应触发事件，以便前端显示新增文件 DiffView")
	}
	if receivedEvent.BackupPath != "" {
		t.Errorf("新文件没有备份，BackupPath 应为空，got %q", receivedEvent.BackupPath)
	}
}

// ── ConfirmModify tests ───────────────────────────────────────────────────

func TestConfirmModify_All(t *testing.T) {
	tmpDir := t.TempDir()
	f1 := createTempFile(t, tmpDir, "a.txt", "aaa")
	f2 := createTempFile(t, tmpDir, "b.txt", "bbb")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir

	s.TrackModify(f1)
	s.TrackModify(f2)

	confirmed, err := s.ConfirmModify()
	if err != nil {
		t.Fatalf("ConfirmModify 不应返回错误: %v", err)
	}
	if len(confirmed) != 2 {
		t.Errorf("应确认 2 个文件, got %d", len(confirmed))
	}

	// 备份文件应被删除
	if fileExists(s.BackupPathFor(f1)) || fileExists(s.BackupPathFor(f2)) {
		t.Error("确认后备份文件应被删除")
	}

	// modifyFiles 应为空
	if s.HasModifyFiles() {
		t.Error("确认后 modifyFiles 应为空")
	}
}

func TestConfirmModify_Selective(t *testing.T) {
	tmpDir := t.TempDir()
	f1 := createTempFile(t, tmpDir, "select_a.txt", "aaa")
	f2 := createTempFile(t, tmpDir, "select_b.txt", "bbb")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir

	s.TrackModify(f1)
	s.TrackModify(f2)

	// 只确认 f1
	confirmed, err := s.ConfirmModify(f1)
	if err != nil {
		t.Fatal(err)
	}
	if len(confirmed) != 1 || confirmed[0] != f1 {
		t.Errorf("选择性确认失败: %+v", confirmed)
	}

	// f2 仍在追踪中
	files := s.GetModifyFiles()
	if len(files) != 1 || files[0] != f2 {
		t.Errorf("f2 应仍在追踪中: %+v", files)
	}
}

func TestConfirmModify_EventFired(t *testing.T) {
	tmpDir := t.TempDir()
	f1 := createTempFile(t, tmpDir, "ev_confirm.txt", "data")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir
	s.TrackModify(f1)

	var receivedEvent FileModifyEvent
	s.SetFileModifyHandler(func(ev FileModifyEvent) {
		receivedEvent = ev
	})

	s.ConfirmModify()

	if receivedEvent.Action != "confirmed" {
		t.Errorf("事件 action 应为 confirmed, got %q", receivedEvent.Action)
	}
	if len(receivedEvent.FilePaths) != 1 {
		t.Errorf("事件 FilePaths 长度: got %d, want 1", len(receivedEvent.FilePaths))
	}
}

// ── Rollback tests ────────────────────────────────────────────────────────

func TestRollback_RestoresContent(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := createTempFile(t, tmpDir, "rollback_test.txt", "original")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir
	s.TrackModify(filePath)

	// 模拟工具修改了文件
	os.WriteFile(filePath, []byte("modified_content"), 0644)

	// 回滚
	rolledBack, err := s.Rollback()
	if err != nil {
		t.Fatalf("Rollback 不应返回错误: %v", err)
	}
	if len(rolledBack) != 1 {
		t.Fatalf("应回滚 1 个文件, got %d", len(rolledBack))
	}

	// 文件应恢复为原始内容
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Errorf("回滚后内容不匹配: got %q, want %q", string(data), "original")
	}

	// 备份文件应被删除
	backupPath := s.BackupPathFor(filePath)
	if fileExists(backupPath) {
		t.Error("回滚后备份文件应被删除")
	}

	// modifyFiles 应为空
	if s.HasModifyFiles() {
		t.Error("回滚后 modifyFiles 应为空")
	}
}

func TestRollback_MultipleFiles(t *testing.T) {
	tmpDir := t.TempDir()
	f1 := createTempFile(t, tmpDir, "multi_a.txt", "aaa")
	f2 := createTempFile(t, tmpDir, "multi_b.txt", "bbb")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir
	s.TrackModify(f1)
	s.TrackModify(f2)

	// 修改两个文件
	os.WriteFile(f1, []byte("mod_a"), 0644)
	os.WriteFile(f2, []byte("mod_b"), 0644)

	rolledBack, err := s.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(rolledBack) != 2 {
		t.Errorf("应回滚 2 个文件, got %d", len(rolledBack))
	}

	// 验证内容恢复
	if data, _ := os.ReadFile(f1); string(data) != "aaa" {
		t.Errorf("f1 内容未恢复: got %q", string(data))
	}
	if data, _ := os.ReadFile(f2); string(data) != "bbb" {
		t.Errorf("f2 内容未恢复: got %q", string(data))
	}
}

func TestRollback_Selective(t *testing.T) {
	tmpDir := t.TempDir()
	f1 := createTempFile(t, tmpDir, "rb_sel_a.txt", "aaa")
	f2 := createTempFile(t, tmpDir, "rb_sel_b.txt", "bbb")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir
	s.TrackModify(f1)
	s.TrackModify(f2)

	os.WriteFile(f1, []byte("mod_a"), 0644)
	os.WriteFile(f2, []byte("mod_b"), 0644)

	// 只回滚 f1
	rolledBack, err := s.Rollback(f1)
	if err != nil {
		t.Fatal(err)
	}
	if len(rolledBack) != 1 {
		t.Errorf("选择性回滚应处理 1 个文件: got %d", len(rolledBack))
	}

	// f1 应恢复
	if data, _ := os.ReadFile(f1); string(data) != "aaa" {
		t.Errorf("f1 未恢复: got %q", string(data))
	}
	// f2 应保持修改状态
	if data, _ := os.ReadFile(f2); string(data) != "mod_b" {
		t.Errorf("f2 应保持修改状态: got %q", string(data))
	}
	// f2 仍在追踪
	if len(s.GetModifyFiles()) != 1 {
		t.Errorf("f2 应仍在追踪中: got %d files", len(s.GetModifyFiles()))
	}
}

func TestRollback_NoBackup(t *testing.T) {
	tmpDir := t.TempDir()
	newFile := filepath.Join(tmpDir, "nobackup.txt")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir
	s.TrackModify(newFile) // 新文件无备份

	rolledBack, err := s.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	if len(rolledBack) != 1 {
		t.Errorf("无备份的文件也可回滚（移除追踪）: got %d", len(rolledBack))
	}
}

func TestRollback_EventFired(t *testing.T) {
	tmpDir := t.TempDir()
	f1 := createTempFile(t, tmpDir, "ev_rb.txt", "data")

	s := newTestSessionWithModify()
	s.projectDir = tmpDir
	s.TrackModify(f1)

	var receivedEvent FileModifyEvent
	s.SetFileModifyHandler(func(ev FileModifyEvent) {
		receivedEvent = ev
	})

	s.Rollback()

	if receivedEvent.Action != "rolled_back" {
		t.Errorf("事件 action 应为 rolled_back, got %q", receivedEvent.Action)
	}
}

// ── GetModifyFiles / HasModifyFiles tests ─────────────────────────────────

func TestGetModifyFiles_ReturnsCopy(t *testing.T) {
	s := newTestSessionWithModify()
	s.modifyFiles = []string{"/a.txt", "/b.txt"}

	files := s.GetModifyFiles()
	files[0] = "/modified"

	// 原始数据不应受影响
	if s.modifyFiles[0] == "/modified" {
		t.Error("GetModifyFiles 应返回副本")
	}
}

func TestHasModifyFiles(t *testing.T) {
	s := newTestSessionWithModify()
	if s.HasModifyFiles() {
		t.Error("空列表应返回 false")
	}
	s.modifyFiles = []string{"/a.txt"}
	if !s.HasModifyFiles() {
		t.Error("非空列表应返回 true")
	}
}

// ── cleanFilePath tests ──────────────────────────────────────────────────

func TestCleanFilePath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/abs/path", "/abs/path"},
		{"./rel/path", ""}, // 依赖当前工作目录，仅检查不 panic
	}
	for _, tt := range tests {
		result := cleanFilePath(tt.in)
		if tt.want != "" && result != tt.want {
			t.Errorf("cleanFilePath(%q) = %q, want %q", tt.in, result, tt.want)
		}
	}
}

// ── mockStore ModifyFiles persistence tests ─────────────────────────────

func TestMockStore_SaveAndGetModifyFiles(t *testing.T) {
	store := newMockStore()

	sessionID := "test-mf-persist"
	files := []string{"/path/a.go", "/path/b.go"}

	err := store.SaveModifyFiles(sessionID, files)
	if err != nil {
		t.Fatal(err)
	}

	got, err := store.GetModifyFiles(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "/path/a.go" || got[1] != "/path/b.go" {
		t.Errorf("GetModifyFiles = %v, want [/path/a.go /path/b.go]", got)
	}
}

func TestMockStore_GetModifyFiles_NotFound(t *testing.T) {
	store := newMockStore()

	got, err := store.GetModifyFiles("nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("不存在 session 应返回 nil, got %v", got)
	}
}

func TestMockStore_DeleteSession_CleansModifyFiles(t *testing.T) {
	store := newMockStore()

	sessionID := "test-mf-delete"
	store.SaveModifyFiles(sessionID, []string{"/a.go"})
	store.DeleteSession(nil, sessionID)

	got, _ := store.GetModifyFiles(sessionID)
	if got != nil {
		t.Errorf("删除 session 后 ModifyFiles 应为 nil, got %v", got)
	}
}
