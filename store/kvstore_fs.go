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

// kvEntry 表示一个带可选过期时间的键值条目。
type kvEntry struct {
	Value     []byte    `json:"value"`
	ExpiresAt time.Time `json:"expires_at,omitempty"`
}

// FileSystemKVStore 使用本地文件系统实现 KVStore。
// 每个会话拥有独立的目录，每个键以 JSON 文件形式存储。
// 通过读写锁保证并发访问的线程安全。
type FileSystemKVStore struct {
	baseDir string
	mu      sync.RWMutex
}

// NewFileSystemKVStore 创建一个新的基于文件系统的 KV 存储。
// 如果 baseDir 为空，则使用默认的临时目录。
// 若基础目录不存在则会创建。
func NewFileSystemKVStore(baseDir string) (*FileSystemKVStore, error) {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "goharness", "kvstore")
	}
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("无法创建 KVStore 基础目录: %w", err)
	}
	return &FileSystemKVStore{baseDir: baseDir}, nil
}

// sessionDir 返回会话存储目录的文件系统路径。
func (s *FileSystemKVStore) sessionDir(sessionID string) string {
	return filepath.Join(s.baseDir, sanitizeSessionID(sessionID))
}

// keyPath 返回会话内特定键的文件系统路径。
func (s *FileSystemKVStore) keyPath(sessionID, key string) string {
	safeKey := sanitizeKey(key)
	return filepath.Join(s.sessionDir(sessionID), safeKey+".json")
}

// Set 在给定会话中存储一个键值对。
// 支持可选 TTL：0 = 不过期，>0 = N 秒后过期，<0 = 立即过期。
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

// Get 从给定会话中获取键对应的值。
// 如果键不存在或已过期则返回 nil（过期条目会被清理）。
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

// Delete 从给定会话中删除一个键。
func (s *FileSystemKVStore) Delete(_ context.Context, sessionID, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := s.keyPath(sessionID, key)
	return os.Remove(path)
}

// ListKeys 返回给定会话中所有未过期的键。
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

// ClearSession 删除会话关联的所有键。
func (s *FileSystemKVStore) ClearSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sessionID)
	return os.RemoveAll(dir)
}

// FileSystemFileStore 使用本地文件系统实现 FileStore。
// 每个会话拥有独立的目录以实现隔离的文件存储。
// 通过校验文件路径来防止路径遍历攻击。
// 通过读写锁保证并发访问的线程安全。
type FileSystemFileStore struct {
	baseDir string
	mu      sync.RWMutex
}

// NewFileSystemFileStore 创建一个新的基于文件系统的文件存储。
// 如果 baseDir 为空，则使用默认的临时目录。
// 若基础目录不存在则会创建。
func NewFileSystemFileStore(baseDir string) (*FileSystemFileStore, error) {
	if baseDir == "" {
		baseDir = filepath.Join(os.TempDir(), "goharness", "filestore")
	}
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("无法创建 FileStore 基础目录: %w", err)
	}
	return &FileSystemFileStore{baseDir: baseDir}, nil
}

// sessionDir 返回会话存储目录的文件系统路径。
func (s *FileSystemFileStore) sessionDir(sessionID string) string {
	return filepath.Join(s.baseDir, sanitizeSessionID(sessionID))
}

// filePath 返回会话内文件的完整文件系统路径。
// 校验路径以防止目录遍历攻击。
func (s *FileSystemFileStore) filePath(sessionID, path string) (string, error) {
	cleanPath := filepath.Clean(path)
	if strings.HasPrefix(cleanPath, "..") || filepath.IsAbs(cleanPath) {
		return "", fmt.Errorf("无效的文件路径: %s", path)
	}
	return filepath.Join(s.sessionDir(sessionID), cleanPath), nil
}

// WriteFile 将内容写入会话内指定路径。
// 按需创建中间目录。
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

// ReadFile 读取并返回会话内文件的内容。
// 如果文件不存在则返回 nil。
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

// DeleteFile 从会话存储中删除文件。
func (s *FileSystemFileStore) DeleteFile(_ context.Context, sessionID, path string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	fullPath, err := s.filePath(sessionID, path)
	if err != nil {
		return err
	}
	return os.Remove(fullPath)
}

// ListFiles 返回会话中匹配给定前缀的所有文件。
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

// ClearSession 删除会话关联的所有文件。
func (s *FileSystemFileStore) ClearSession(_ context.Context, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir := s.sessionDir(sessionID)
	return os.RemoveAll(dir)
}

// GetSessionPath 返回会话存储目录的文件系统路径。
func (s *FileSystemFileStore) GetSessionPath(sessionID string) string {
	return s.sessionDir(sessionID)
}

// sanitizeSessionID 对会话 ID 进行清理以用作目录名。
// 将任何非字母数字字符（除 - 和 _ 外）替换为下划线。
func sanitizeSessionID(id string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, id)
}

// sanitizeKey 对键进行清理以用作文件名。
// 将任何非字母数字字符（除 -、_ 和 . 外）替换为下划线。
func sanitizeKey(key string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '_'
	}, key)
}
