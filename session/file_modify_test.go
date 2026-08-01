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
	backupPath := filepath.Join(s.resolveBackupDir(), "test.txt.bak")
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
	backupPath := filepath.Join(s.resolveBackupDir(), "new_file.txt.bak")
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
	backupPath := filepath.Join(s.resolveBackupDir(), "dup.txt.bak")
	data, _ := os.ReadFile(backupPath)
	if string(data) != "content" {
		t.Errorf("备份应保持首次内容: got %q, want %q", string(data), "content")
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
	backupDir := s.resolveBackupDir()
	if fileExists(filepath.Join(backupDir, "a.txt.bak")) || fileExists(filepath.Join(backupDir, "b.txt.bak")) {
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
	backupPath := filepath.Join(s.resolveBackupDir(), "rollback_test.txt.bak")
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
		{"./rel/path", ""}, // depends on cwd, just check no panic
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
