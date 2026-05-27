package store

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileSystemKVStore_BasicOperations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kvstore-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewFileSystemKVStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create KVStore: %v", err)
	}

	ctx := context.Background()
	sessionID := "test-session-1"
	key := "test-key"
	value := []byte("test-value")

	if err := store.Set(ctx, sessionID, key, value, 0); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	got, err := store.Get(ctx, sessionID, key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(got) != string(value) {
		t.Errorf("Get() = %s, want %s", string(got), string(value))
	}

	keys, err := store.ListKeys(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListKeys failed: %v", err)
	}
	if len(keys) != 1 || keys[0] != key {
		t.Errorf("ListKeys() = %v, want [%s]", keys, key)
	}

	if err := store.Delete(ctx, sessionID, key); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	got, err = store.Get(ctx, sessionID, key)
	if err != nil {
		t.Fatalf("Get after Delete failed: %v", err)
	}
	if got != nil {
		t.Errorf("Get() after Delete should be nil, got %v", got)
	}
}

func TestFileSystemKVStore_TTL(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kvstore-ttl-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewFileSystemKVStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create KVStore: %v", err)
	}

	ctx := context.Background()
	sessionID := "test-session-ttl"
	key := "expiring-key"
	value := []byte("expiring-value")

	if err := store.Set(ctx, sessionID, key, value, -1); err != nil {
		t.Fatalf("Set with TTL failed: %v", err)
	}

	got, err := store.Get(ctx, sessionID, key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if got != nil {
		t.Errorf("Get() should return nil for expired key, got %v", got)
	}
}

func TestFileSystemKVStore_ClearSession(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kvstore-clear-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewFileSystemKVStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create KVStore: %v", err)
	}

	ctx := context.Background()
	sessionID := "test-session-clear"

	for i := 0; i < 3; i++ {
		if err := store.Set(ctx, sessionID, "key-"+string(rune('0'+i)), []byte("value"), 0); err != nil {
			t.Fatalf("Set failed: %v", err)
		}
	}

	if err := store.ClearSession(ctx, sessionID); err != nil {
		t.Fatalf("ClearSession failed: %v", err)
	}

	keys, err := store.ListKeys(ctx, sessionID)
	if err != nil {
		t.Fatalf("ListKeys after ClearSession failed: %v", err)
	}
	if len(keys) != 0 {
		t.Errorf("ListKeys() after ClearSession should be empty, got %v", keys)
	}
}

func TestFileSystemKVStore_SessionIsolation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "kvstore-isolation-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewFileSystemKVStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create KVStore: %v", err)
	}

	ctx := context.Background()

	session1 := "session-1"
	session2 := "session-2"

	if err := store.Set(ctx, session1, "shared-key", []byte("value-1"), 0); err != nil {
		t.Fatalf("Set for session1 failed: %v", err)
	}
	if err := store.Set(ctx, session2, "shared-key", []byte("value-2"), 0); err != nil {
		t.Fatalf("Set for session2 failed: %v", err)
	}

	v1, err := store.Get(ctx, session1, "shared-key")
	if err != nil {
		t.Fatalf("Get for session1 failed: %v", err)
	}
	if string(v1) != "value-1" {
		t.Errorf("Get(session1) = %s, want value-1", string(v1))
	}

	v2, err := store.Get(ctx, session2, "shared-key")
	if err != nil {
		t.Fatalf("Get for session2 failed: %v", err)
	}
	if string(v2) != "value-2" {
		t.Errorf("Get(session2) = %s, want value-2", string(v2))
	}
}

func TestFileSystemKVStore_SanitizeSessionID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"session-123", "session-123"},
		{"session_456", "session_456"},
		{"session/with/slashes", "session_with_slashes"},
		{"session with spaces", "session_with_spaces"},
		{"session#special!chars", "session_special_chars"},
	}

	for _, test := range tests {
		result := sanitizeSessionID(test.input)
		if result != test.expected {
			t.Errorf("sanitizeSessionID(%q) = %q, want %q", test.input, result, test.expected)
		}
	}
}

func TestFileSystemFileStore_BasicOperations(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filestore-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewFileSystemFileStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create FileStore: %v", err)
	}

	ctx := context.Background()
	sessionID := "test-file-session"
	filePath := "test.txt"
	content := "Hello, World!"

	if err := store.WriteFile(ctx, sessionID, filePath, strings.NewReader(content)); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	reader, err := store.ReadFile(ctx, sessionID, filePath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if reader == nil {
		t.Fatal("ReadFile returned nil reader")
	}
	defer reader.Close()

	buf := new(strings.Builder)
	if _, err := io.Copy(buf, reader); err != nil {
		t.Fatalf("Failed to read from reader: %v", err)
	}
	if buf.String() != content {
		t.Errorf("ReadFile() = %q, want %q", buf.String(), content)
	}

	files, err := store.ListFiles(ctx, sessionID, "test")
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}
	if len(files) != 1 || files[0] != "test.txt" {
		t.Errorf("ListFiles() = %v, want [test.txt]", files)
	}

	if err := store.DeleteFile(ctx, sessionID, filePath); err != nil {
		t.Fatalf("DeleteFile failed: %v", err)
	}

	reader, err = store.ReadFile(ctx, sessionID, filePath)
	if err != nil {
		t.Fatalf("ReadFile after DeleteFile failed: %v", err)
	}
	if reader != nil {
		t.Errorf("ReadFile() after DeleteFile should be nil, got %v", reader)
	}
}

func TestFileSystemFileStore_PathTraversalProtection(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filestore-security-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewFileSystemFileStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create FileStore: %v", err)
	}

	ctx := context.Background()
	sessionID := "test-security-session"

	unsafePaths := []string{
		"../../../etc/passwd",
		"/etc/hosts",
		"../../tmp/malicious.txt",
	}

	for _, path := range unsafePaths {
		err := store.WriteFile(ctx, sessionID, path, strings.NewReader("malicious content"))
		if err == nil {
			t.Errorf("WriteFile(%q) should fail for security reasons, but succeeded", path)
		}
	}
}

func TestFileSystemFileStore_Subdirectories(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filestore-subdir-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewFileSystemFileStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create FileStore: %v", err)
	}

	ctx := context.Background()
	sessionID := "test-subdir-session"
	nestedPath := "subdir/nested/file.txt"
	content := "nested content"

	if err := store.WriteFile(ctx, sessionID, nestedPath, strings.NewReader(content)); err != nil {
		t.Fatalf("WriteFile to nested path failed: %v", err)
	}

	reader, err := store.ReadFile(ctx, sessionID, nestedPath)
	if err != nil {
		t.Fatalf("ReadFile from nested path failed: %v", err)
	}
	if reader == nil {
		t.Fatal("ReadFile returned nil reader")
	}
	defer reader.Close()

	buf := new(strings.Builder)
	if _, err := io.Copy(buf, reader); err != nil {
		t.Fatalf("Failed to read from reader: %v", err)
	}
	if buf.String() != content {
		t.Errorf("ReadFile() = %q, want %q", buf.String(), content)
	}
}

func TestFileSystemFileStore_ClearSession(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "filestore-clear-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	store, err := NewFileSystemFileStore(tmpDir)
	if err != nil {
		t.Fatalf("Failed to create FileStore: %v", err)
	}

	ctx := context.Background()
	sessionID := "test-clear-session"

	for i := 0; i < 3; i++ {
		path := "file-" + string(rune('0'+i)) + ".txt"
		if err := store.WriteFile(ctx, sessionID, path, strings.NewReader("content")); err != nil {
			t.Fatalf("WriteFile failed: %v", err)
		}
	}

	if err := store.ClearSession(ctx, sessionID); err != nil {
		t.Fatalf("ClearSession failed: %v", err)
	}

	sessionDir := filepath.Join(store.baseDir, sanitizeSessionID(sessionID))
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Errorf("Session directory should not exist after ClearSession, err: %v", err)
	}
}
