// Package store 提供文件和键值操作的存储抽象。
// 它包含文件存储、KV 存储和异步任务结果存储的接口，
// 以及基于文件系统的实现。
package store

import (
	"context"
	"io"
)

// FileStore 定义会话级文件存储操作的接口。
// 实现为每个会话提供隔离的文件存储，支持
// 标准 CRUD 操作和会话清理。
type FileStore interface {
	// WriteFile 将内容写入会话内指定路径。
	WriteFile(ctx context.Context, sessionID, path string, content io.Reader) error
	// ReadFile 读取并返回会话内文件的内容。
	ReadFile(ctx context.Context, sessionID, path string) (io.ReadCloser, error)
	// DeleteFile 从会话存储中删除文件。
	DeleteFile(ctx context.Context, sessionID, path string) error
	// ListFiles 返回会话中匹配给定前缀的所有文件。
	ListFiles(ctx context.Context, sessionID, prefix string) ([]string, error)
	// ClearSession 删除会话关联的所有文件。
	ClearSession(ctx context.Context, sessionID string) error
	// GetSessionPath 返回会话存储目录的文件系统路径。
	GetSessionPath(sessionID string) string
}
