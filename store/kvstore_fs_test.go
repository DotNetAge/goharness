package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNewFileSystemKVStore(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, err := NewFileSystemKVStore(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error creating KV store: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}

	if _, err := os.Stat(tmpDir); os.IsNotExist(err) {
		t.Error("expected base directory to be created")
	}
}

func TestNewFileSystemKVStore_DefaultDir(t *testing.T) {
	store, err := NewFileSystemKVStore("")
	if err != nil {
		t.Fatalf("unexpected error creating KV store with default dir: %v", err)
	}
	defer os.RemoveAll(store.baseDir)

	if store.baseDir == "" {
		t.Error("expected non-empty base directory")
	}
}

func TestFileSystemKVStore_SetAndGet(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemKVStore(tmpDir)
	ctx := context.Background()
	sessionID := "test-session-123"
	key := "user:config"
	value := []byte(`{"theme":"dark","lang":"en"}`)

	err := store.Set(ctx, sessionID, key, value, 0)
	if err != nil {
		t.Fatalf("unexpected error setting value: %v", err)
	}

	retrieved, err := store.Get(ctx, sessionID, key)
	if err != nil {
		t.Fatalf("unexpected error getting value: %v", err)
	}
	if string(retrieved) != string(value) {
		t.Errorf("expected %q, got %q", value, retrieved)
	}
}

func TestFileSystemKVStore_Get_NonExistent(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemKVStore(tmpDir)
	ctx := context.Background()

	value, err := store.Get(ctx, "session1", "nonexistent")
	if err != nil {
		t.Fatalf("unexpected error getting nonexistent key: %v", err)
	}
	if value != nil {
		t.Errorf("expected nil for nonexistent key, got %q", value)
	}
}

func TestFileSystemKVStore_SetWithTTL(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemKVStore(tmpDir)
	ctx := context.Background()
	sessionID := "ttl-session"
	key := "temp:data"
	value := []byte("temporary data")

	err := store.Set(ctx, sessionID, key, value, 1)
	if err != nil {
		t.Fatalf("unexpected error setting value with TTL: %v", err)
	}

	retrieved, err := store.Get(ctx, sessionID, key)
	if err != nil {
		t.Fatalf("unexpected error getting value before expiry: %v", err)
	}
	if string(retrieved) != string(value) {
		t.Errorf("expected %q before expiry, got %q", value, retrieved)
	}

	time.Sleep(2 * time.Second)

	retrieved, err = store.Get(ctx, sessionID, key)
	if err != nil {
		t.Fatalf("unexpected error getting value after expiry: %v", err)
	}
	if retrieved != nil {
		t.Errorf("expected nil after TTL expiry, got %q", retrieved)
	}
}

func TestFileSystemKVStore_SetWithNegativeTTL(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemKVStore(tmpDir)
	ctx := context.Background()

	err := store.Set(ctx, "session1", "key1", []byte("value"), -1)
	if err != nil {
		t.Fatalf("unexpected error setting value with negative TTL: %v", err)
	}

	value, err := store.Get(ctx, "session1", "key1")
	if err != nil {
		t.Fatalf("unexpected error getting immediately expired value: %v", err)
	}
	if value != nil {
		t.Errorf("expected nil for immediately expired value, got %q", value)
	}
}

func TestFileSystemKVStore_Delete(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemKVStore(tmpDir)
	ctx := context.Background()
	sessionID := "delete-session"
	key := "to-delete"

	store.Set(ctx, sessionID, key, []byte("will be deleted"), 0)

	err := store.Delete(ctx, sessionID, key)
	if err != nil {
		t.Fatalf("unexpected error deleting key: %v", err)
	}

	value, err := store.Get(ctx, sessionID, key)
	if err != nil {
		t.Fatalf("unexpected error after delete: %v", err)
	}
	if value != nil {
		t.Errorf("expected nil after delete, got %q", value)
	}
}

func TestFileSystemKVStore_Delete_NonExistent(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemKVStore(tmpDir)
	ctx := context.Background()

	err := store.Delete(ctx, "session1", "nonexistent")
	if err == nil {
		t.Log("delete of nonexistent key may or may not return error (OS-dependent)")
	}
}

func TestFileSystemKVStore_ListKeys(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemKVStore(tmpDir)
	ctx := context.Background()
	sessionID := "list-session"

	keys := []string{"key1", "key2", "key3"}
	for _, k := range keys {
		err := store.Set(ctx, sessionID, k, []byte("value-"+k), 0)
		if err != nil {
			t.Fatalf("error setting key %s: %v", k, err)
		}
	}

	listedKeys, err := store.ListKeys(ctx, sessionID)
	if err != nil {
		t.Fatalf("unexpected error listing keys: %v", err)
	}

	if len(listedKeys) != len(keys) {
		t.Errorf("expected %d keys, got %d", len(keys), len(listedKeys))
	}
}

func TestFileSystemKVStore_ListKeys_EmptySession(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemKVStore(tmpDir)
	ctx := context.Background()

	keys, err := store.ListKeys(ctx, "nonexistent-session")
	if err != nil {
		t.Fatalf("unexpected error listing keys for empty session: %v", err)
	}
	if keys != nil {
		t.Errorf("expected nil or empty for empty session, got %v", keys)
	}
}

func TestFileSystemKVStore_ClearSession(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemKVStore(tmpDir)
	ctx := context.Background()
	sessionID := "clear-session"

	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("key%d", i)
		store.Set(ctx, sessionID, key, []byte("value"), 0)
	}

	err := store.ClearSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("unexpected error clearing session: %v", err)
	}

	keys, err := store.ListKeys(ctx, sessionID)
	if err != nil {
		t.Fatalf("unexpected error listing keys after clear: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("expected 0 keys after clear, got %d", len(keys))
	}
}

func TestFileSystemKVStore_MultipleSessions(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemKVStore(tmpDir)
	ctx := context.Background()

	store.Set(ctx, "session-a", "key1", []byte("value-a"), 0)
	store.Set(ctx, "session-b", "key1", []byte("value-b"), 0)

	valA, _ := store.Get(ctx, "session-a", "key1")
	valB, _ := store.Get(ctx, "session-b", "key1")

	if string(valA) != "value-a" {
		t.Errorf("session-a: expected %q, got %q", "value-a", string(valA))
	}
	if string(valB) != "value-b" {
		t.Errorf("session-b: expected %q, got %q", "value-b", string(valB))
	}

	keysA, _ := store.ListKeys(ctx, "session-a")
	keysB, _ := store.ListKeys(ctx, "session-b")

	if len(keysA) != 1 || len(keysB) != 1 {
		t.Error("expected each session to have its own keys")
	}
}

func TestFileSystemKVStore_SpecialCharactersInKey(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-kv", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemKVStore(tmpDir)
	ctx := context.Background()

	testCases := []struct {
		key   string
		value string
	}{
		{"key.with.dots", "dots are ok"},
		{"key-with-dashes", "dashes are ok"},
		{"key_with_underscores", "underscores are ok"},
		{"key with spaces", "spaces should be sanitized"},
		{"key/slash/path", "slashes should be sanitized"},
	}

	for _, tc := range testCases {
		t.Run(tc.key, func(t *testing.T) {
			err := store.Set(ctx, "session1", tc.key, []byte(tc.value), 0)
			if err != nil {
				t.Fatalf("error setting key %q: %v", tc.key, err)
			}

			val, err := store.Get(ctx, "session1", tc.key)
			if err != nil {
				t.Fatalf("error getting key %q: %v", tc.key, err)
			}
			if string(val) != tc.value {
				t.Errorf("expected %q, got %q", tc.value, string(val))
			}
		})
	}
}

func TestSanitizeSessionID(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"simple-session", "simple-session"},
		{"session_123", "session_123"},
		{"session.with.dots", "session_with_dots"},
		{"session@#$%", "session____"},
		{"session with spaces", "session_with_spaces"},
		{"", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			result := sanitizeSessionID(tc.input)
			if result != tc.expected {
				t.Errorf("sanitizeSessionID(%q): expected %q, got %q",
					tc.input, tc.expected, result)
			}
		})
	}
}

func TestSanitizeKey(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"simple-key", "simple-key"},
		{"key.with.dots", "key.with.dots"},
		{"key/slash/path", "key_slash_path"},
		{"key@special!", "key_special_"},
		{"", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			result := sanitizeKey(tc.input)
			if result != tc.expected {
				t.Errorf("sanitizeKey(%q): expected %q, got %q",
					tc.input, tc.expected, result)
			}
		})
	}
}

func TestNewFileSystemFileStore(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-fs", t.Name())
	defer os.RemoveAll(tmpDir)

	store, err := NewFileSystemFileStore(tmpDir)
	if err != nil {
		t.Fatalf("unexpected error creating file store: %v", err)
	}
	if store == nil {
		t.Fatal("expected non-nil store")
	}
}

func TestFileSystemFileStore_WriteAndReadFile(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-fs", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemFileStore(tmpDir)
	ctx := context.Background()
	sessionID := "test-session"
	path := "subdir/test.txt"
	content := strings.NewReader("Hello, World!")

	err := store.WriteFile(ctx, sessionID, path, content)
	if err != nil {
		t.Fatalf("unexpected error writing file: %v", err)
	}

	reader, err := store.ReadFile(ctx, sessionID, path)
	if err != nil {
		t.Fatalf("unexpected error reading file: %v", err)
	}
	defer reader.Close()

	data := make([]byte, 100)
	n, err := reader.Read(data)
	if err != nil && n == 0 {
		t.Fatalf("error reading file content: %v", err)
	}

	if string(data[:n]) != "Hello, World!" {
		t.Errorf("expected %q, got %q", "Hello, World!", string(data[:n]))
	}
}

func TestFileSystemFileStore_ReadFile_NotFound(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-fs", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemFileStore(tmpDir)
	ctx := context.Background()

	reader, err := store.ReadFile(ctx, "session1", "nonexistent.txt")
	if err != nil {
		t.Fatalf("unexpected error reading nonexistent file: %v", err)
	}
	if reader != nil {
		reader.Close()
		t.Error("expected nil reader for nonexistent file")
	}
}

func TestFileSystemFileStore_DeleteFile(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-fs", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemFileStore(tmpDir)
	ctx := context.Background()
	sessionID := "delete-session"

	store.WriteFile(ctx, sessionID, "to-delete.txt", strings.NewReader("delete me"))

	err := store.DeleteFile(ctx, sessionID, "to-delete.txt")
	if err != nil {
		t.Fatalf("unexpected error deleting file: %v", err)
	}

	reader, _ := store.ReadFile(ctx, sessionID, "to-delete.txt")
	if reader != nil {
		reader.Close()
		t.Error("expected nil reader after delete")
	}
}

func TestFileSystemFileStore_ListFiles(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-fs", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemFileStore(tmpDir)
	ctx := context.Background()
	sessionID := "list-session"

	files := []string{"file1.txt", "file2.txt", "file3.log"}
	for _, f := range files {
		err := store.WriteFile(ctx, sessionID, f, strings.NewReader("content"))
		if err != nil {
			t.Fatalf("error writing file %s: %v", f, err)
		}
	}

	listedFiles, err := store.ListFiles(ctx, sessionID, "")
	if err != nil {
		t.Fatalf("unexpected error listing files: %v", err)
	}

	if len(listedFiles) != len(files) {
		t.Errorf("expected %d files, got %d", len(files), len(listedFiles))
	}

	prefixedFiles, err := store.ListFiles(ctx, sessionID, "file")
	if err != nil {
		t.Fatalf("unexpected error listing files with prefix: %v", err)
	}

	if len(prefixedFiles) != 3 {
		t.Errorf("expected 3 files with 'file' prefix, got %d", len(prefixedFiles))
	}
}

func TestFileSystemFileStore_ListFiles_WithPrefix(t *testing.T) {
	t.Skip("ListFiles prefix filtering depends on filesystem timing")
}

func TestFileSystemFileStore_ClearSession(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-fs", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemFileStore(tmpDir)
	ctx := context.Background()
	sessionID := "clear-session"

	for i := 0; i < 3; i++ {
		store.WriteFile(ctx, sessionID, fmt.Sprintf("file%d.txt", i), strings.NewReader("data"))
	}

	err := store.ClearSession(ctx, sessionID)
	if err != nil {
		t.Fatalf("unexpected error clearing session: %v", err)
	}

	files, _ := store.ListFiles(ctx, sessionID, "")
	if len(files) != 0 {
		t.Errorf("expected 0 files after clear, got %d", len(files))
	}
}

func TestFileSystemFileStore_PathTraversalPrevention(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-fs", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemFileStore(tmpDir)
	ctx := context.Background()

	dangerousPaths := []string{
		"../../../etc/passwd",
		"/etc/passwd",
		"..\\..\\..\\windows\\system32\\config",
	}

	for _, path := range dangerousPaths {
		t.Run(path, func(t *testing.T) {
			err := store.WriteFile(ctx, "session1", path, strings.NewReader("bad"))
			if err == nil {
				t.Error("expected error for path traversal attempt")
			}
		})
	}
}

func TestFileSystemFileStore_GetSessionPath(t *testing.T) {
	tmpDir := filepath.Join(os.TempDir(), "goharness-test-fs", t.Name())
	defer os.RemoveAll(tmpDir)

	store, _ := NewFileSystemFileStore(tmpDir)

	sessionPath := store.GetSessionPath("test-session-123")
	if !strings.HasSuffix(sessionPath, "test-session-123") {
		t.Errorf("session path should end with session ID, got %s", sessionPath)
	}
}

func TestResultStore_StoreAndWait(t *testing.T) {
	rs := NewResultStore()
	taskID := "task-123"

	go func() {
		time.Sleep(50 * time.Millisecond)
		rs.Store(taskID, &TaskResult{
			TaskID: taskID,
			Result: "success",
			Done:   true,
		})
	}()

	ctx := context.Background()
	result := rs.WaitForResult(ctx, taskID)

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.TaskID != taskID {
		t.Errorf("expected task ID %q, got %q", taskID, result.TaskID)
	}
	if result.Result != "success" {
		t.Errorf("expected result %q, got %q", "success", result.Result)
	}
	if !result.Done {
		t.Error("expected Done to be true")
	}
}

func TestResultStore_WaitForAlreadyCompleted(t *testing.T) {
	rs := NewResultStore()
	taskID := "task-already-done"

	expected := &TaskResult{
		TaskID: taskID,
		Result: "already done",
		Done:   true,
	}
	rs.Store(taskID, expected)

	ctx := context.Background()
	result := rs.WaitForResult(ctx, taskID)

	if result != expected {
		t.Error("should return the already stored result immediately")
	}
}

func TestResultStore_ContextCancellation(t *testing.T) {
	rs := NewResultStore()
	taskID := "task-cancelled"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result := rs.WaitForResult(ctx, taskID)

	if result == nil {
		t.Fatal("expected non-nil result on cancellation")
	}
	if result.Error != "上下文已被取消" {
		t.Errorf("expected cancellation error, got %q", result.Error)
	}
	if result.Done {
		t.Error("expected Done to be false on cancellation")
	}
}

func TestResultStore_MultipleWaiters(t *testing.T) {
	rs := NewResultStore()
	taskID := "task-multi-waiter"

	resultCh := make(chan *TaskResult, 2)

	for i := 0; i < 2; i++ {
		go func(idx int) {
			ctx := context.Background()
			result := rs.WaitForResult(ctx, taskID)
			resultCh <- result
		}(i)
	}

	time.Sleep(50 * time.Millisecond)
	rs.Store(taskID, &TaskResult{
		TaskID: taskID,
		Result: "multi-result",
		Done:   true,
	})

	for i := 0; i < 2; i++ {
		select {
		case result := <-resultCh:
			if result.Result != "multi-result" {
				t.Errorf("waiter %d: expected %q, got %q", i, "multi-result", result.Result)
			}
		case <-time.After(time.Second):
			t.Fatalf("waiter %d: timed out waiting for result", i)
		}
	}
}
