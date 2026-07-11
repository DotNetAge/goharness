package store

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// kvEntry represents a single key-value entry with optional expiration.
type kvEntry struct {
	Value     []byte    `json:"value"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// FileSystemKVStore implements KVStore using the local filesystem.
// Each session gets its own directory, and each key is stored as a JSON file.
// Thread-safe with read-write locking for concurrent access.
type FileSystemKVStore struct {
	baseDir string
	mu      sync.RWMutex
}

// NewFileSystemKVStore creates a new filesystem-based KV store.
// If baseDir is empty, uses a default temp directory.
// Creates the base directory if it doesn't exist.
func NewFileSystemKVStore(baseDir string) (*FileSystemKVStore, error) {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "goharness", "kvstore")
	}
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("无法创建 KVStore 基础目录: %w", err)
	}
	return &FileSystemKVStore{baseDir: baseDir}, nil
}

// sessionDir returns the filesystem path for a session's storage directory.
func (s *FileSystemKVStore) sessionDir(sessionID string) string {
	return filepath.Join(s.baseDir, sanitizeSessionID(sessionID))
}

// keyPath returns the filesystem path for a specific key within a session.
func (s *FileSystemKVStore) keyPath(sessionID, key string) string {
	safeKey := sanitizeKey(key)
	return filepath.Join(s.sessionDir(sessionID), safeKey+".json")
}

// Set stores a key-value pair in the given session.
// Supports optional TTL: 0 = no expiry, >0 = expire after N seconds, <0 = immediately expire.
func (s *FileSystemKVStore) Set(_ context.Context, sessionID, key string, value []byte, ttlSeconds int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("无法创建会话目录: %w", err)
	}

	entry := kvEntry{Value: value}
	if ttlSeconds < 0 {
		entry.ExpiresAt = time.Now().Add(-time.Second)
	} else if ttlSeconds > 0 {
		entry.ExpiresAt = time.Now().Add(time.Duration(ttlSeconds) * time.Second)
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("无法序列化 KV 条目: %w", err)
	}

	return os.WriteFile(s.keyPath(sessionID, key), data, 0644)
}

// Get retrieves the value for a key from the given session.
// Returns nil if the key doesn't exist or has expired (expired entries are cleaned up).
func (s *FileSystemKVStore) Get(_ context.Context, sessionID, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.keyPath(sessionID, key)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("无法读取 KV 条目: %w", err)
	}

	var entry kvEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("无法解析 KV 条目: %w", err)
	}

	if !entry.ExpiresAt.IsZero() && time.Now().After(entry.ExpiresAt) {
		s.mu.RUnlock()
		s.mu.Lock()
		os.Remove(path)
		s.mu.Unlock()
		s.mu.RLock()
		return nil, nil
	}

	return entry.Value, nil
}

// Delete removes a key from the given session.
func (s *FileSystemKVStore) Delete(_ context.Context, sessionID, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.keyPath(sessionID, key)
	return os.Remove(path)
}

// ListKeys returns all non-expired keys in the given session.
func (s *FileSystemKVStore) ListKeys(_ context.Context, sessionID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := s.sessionDir(sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("无法读取会话目录: %w", err)
	}

	var keys []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if len(name) > 5 && name[len(name)-5:] == ".json" {
			keys = append(keys, name[:len(name)-5])
		}
	}
	return keys, nil
}

// ClearSession removes all keys associated with a session.
func (s *FileSystemKVStore) ClearSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sessionID)
	return os.RemoveAll(dir)
}

// FileSystemFileStore implements FileStore using the local filesystem.
// Each session gets its own directory for isolated file storage.
// Path traversal attacks are prevented by validating file paths.
// Thread-safe with read-write locking for concurrent access.
type FileSystemFileStore struct {
	baseDir string
	mu      sync.RWMutex
}

// NewFileSystemFileStore creates a new filesystem-based file store.
// If baseDir is empty, uses a default temp directory.
// Creates the base directory if it doesn't exist.
func NewFileSystemFileStore(baseDir string) (*FileSystemFileStore, error) {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "goharness", "filestore")
	}
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("无法创建 FileStore 基础目录: %w", err)
	}
	return &FileSystemFileStore{baseDir: baseDir}, nil
}

// sessionDir returns the filesystem path for a session's storage directory.
func (s *FileSystemFileStore) sessionDir(sessionID string) string {
	return filepath.Join(s.baseDir, sanitizeSessionID(sessionID))
}

// filePath returns the full filesystem path for a file within a session.
// Validates the path to prevent directory traversal attacks.
func (s *FileSystemFileStore) filePath(sessionID, path string) (string, error) {
	cleanPath := filepath.Clean(path)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		return "", fmt.Errorf("无效的文件路径: %s", path)
	}
	return filepath.Join(s.sessionDir(sessionID), cleanPath), nil
}

// WriteFile writes content to the specified path within a session.
// Creates intermediate directories as needed.
func (s *FileSystemFileStore) WriteFile(_ context.Context, sessionID, path string, content io.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("无法创建会话目录: %w", err)
	}

	fullPath, err := s.filePath(sessionID, path)
	if err != nil {
		return err
	}

	dirPath := filepath.Dir(fullPath)
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return fmt.Errorf("无法创建子目录: %w", err)
	}

	f, err := os.Create(fullPath)
	if err != nil {
		return fmt.Errorf("无法创建文件: %w", err)
	}
	defer f.Close()

	_, err = io.Copy(f, content)
	return err
}

// ReadFile reads and returns the content of a file within a session.
// Returns nil if the file doesn't exist.
func (s *FileSystemFileStore) ReadFile(_ context.Context, sessionID, path string) (io.ReadCloser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fullPath, err := s.filePath(sessionID, path)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("无法打开文件: %w", err)
	}
	return f, nil
}

// DeleteFile removes a file from the session storage.
func (s *FileSystemFileStore) DeleteFile(_ context.Context, sessionID, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fullPath, err := s.filePath(sessionID, path)
	if err != nil {
		return err
	}
	return os.Remove(fullPath)
}

// ListFiles returns all files in the session that match the given prefix.
func (s *FileSystemFileStore) ListFiles(_ context.Context, sessionID, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := s.sessionDir(sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("无法读取会话目录: %w", err)
	}

	var files []string
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, prefix) {
			files = append(files, name)
		}
	}
	return files, nil
}

// ClearSession removes all files associated with a session.
func (s *FileSystemFileStore) ClearSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sessionID)
	return os.RemoveAll(dir)
}

// GetSessionPath returns the filesystem path for a session's storage directory.
func (s *FileSystemFileStore) GetSessionPath(sessionID string) string {
	return s.sessionDir(sessionID)
}

// sanitizeSessionID sanitizes a session ID for use as a directory name.
// Replaces any non-alphanumeric characters (except - and _) with underscores.
func sanitizeSessionID(id string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
}

// sanitizeKey sanitizes a key for use as a filename.
// Replaces any non-alphanumeric characters (except -, _, and .) with underscores.
func sanitizeKey(key string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, key)
}
