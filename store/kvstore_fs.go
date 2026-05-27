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

type kvEntry struct {
	Value     []byte    `json:"value"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

type FileSystemKVStore struct {
	baseDir string
	mu      sync.RWMutex
}

func NewFileSystemKVStore(baseDir string) (*FileSystemKVStore, error) {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "goreact", "kvstore")
	}
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create KVStore base directory: %w", err)
	}
	return &FileSystemKVStore{baseDir: baseDir}, nil
}

func (s *FileSystemKVStore) sessionDir(sessionID string) string {
	return filepath.Join(s.baseDir, sanitizeSessionID(sessionID))
}

func (s *FileSystemKVStore) keyPath(sessionID, key string) string {
	safeKey := sanitizeKey(key)
	return filepath.Join(s.sessionDir(sessionID), safeKey+".json")
}

func (s *FileSystemKVStore) Set(_ context.Context, sessionID, key string, value []byte, ttlSeconds int) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	entry := kvEntry{Value: value}
	if ttlSeconds < 0 {
		entry.ExpiresAt = time.Now().Add(-time.Second)
	} else if ttlSeconds > 0 {
		entry.ExpiresAt = time.Now().Add(time.Duration(ttlSeconds) * time.Second)
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("failed to marshal KV entry: %w", err)
	}

	return os.WriteFile(s.keyPath(sessionID, key), data, 0644)
}

func (s *FileSystemKVStore) Get(_ context.Context, sessionID, key string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	path := s.keyPath(sessionID, key)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read KV entry: %w", err)
	}

	var entry kvEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, fmt.Errorf("failed to unmarshal KV entry: %w", err)
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

func (s *FileSystemKVStore) Delete(_ context.Context, sessionID, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.keyPath(sessionID, key)
	return os.Remove(path)
}

func (s *FileSystemKVStore) ListKeys(_ context.Context, sessionID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := s.sessionDir(sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read session directory: %w", err)
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

func (s *FileSystemKVStore) ClearSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sessionID)
	return os.RemoveAll(dir)
}

type FileSystemFileStore struct {
	baseDir string
	mu      sync.RWMutex
}

func NewFileSystemFileStore(baseDir string) (*FileSystemFileStore, error) {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "goreact", "filestore")
	}
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create FileStore base directory: %w", err)
	}
	return &FileSystemFileStore{baseDir: baseDir}, nil
}

func (s *FileSystemFileStore) sessionDir(sessionID string) string {
	return filepath.Join(s.baseDir, sanitizeSessionID(sessionID))
}

func (s *FileSystemFileStore) filePath(sessionID, path string) (string, error) {
	cleanPath := filepath.Clean(path)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		return "", fmt.Errorf("invalid file path: %s", path)
	}
	return filepath.Join(s.sessionDir(sessionID), cleanPath), nil
}

func (s *FileSystemFileStore) WriteFile(_ context.Context, sessionID, path string, content io.Reader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sessionID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create session directory: %w", err)
	}

	fullPath, err := s.filePath(sessionID, path)
	if err != nil {
		return err
	}

	dirPath := filepath.Dir(fullPath)
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return fmt.Errorf("failed to create subdirectory: %w", err)
	}

	f, err := os.Create(fullPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer f.Close()

	_, err = io.Copy(f, content)
	return err
}

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
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	return f, nil
}

func (s *FileSystemFileStore) DeleteFile(_ context.Context, sessionID, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fullPath, err := s.filePath(sessionID, path)
	if err != nil {
		return err
	}
	return os.Remove(fullPath)
}

func (s *FileSystemFileStore) ListFiles(_ context.Context, sessionID, prefix string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir := s.sessionDir(sessionID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to read session directory: %w", err)
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

func (s *FileSystemFileStore) ClearSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sessionID)
	return os.RemoveAll(dir)
}

func (s *FileSystemFileStore) GetSessionPath(sessionID string) string {
	return s.sessionDir(sessionID)
}

func sanitizeSessionID(id string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
}

func sanitizeKey(key string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, key)
}
