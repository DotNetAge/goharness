// Package store provides storage abstractions for file and key-value operations.
// It includes interfaces for file storage, KV storage, and async task result storage,
// along with filesystem-based implementations.
package store

import (
	"context"
	"io"
)

// FileStore defines the interface for session-scoped file storage operations.
// Implementations provide isolated file storage per session with support for
// standard CRUD operations and session cleanup.
type FileStore interface {
	// WriteFile writes content to the specified path within a session.
	WriteFile(ctx context.Context, sessionID, path string, content io.Reader) error
	// ReadFile reads and returns the content of a file within a session.
	ReadFile(ctx context.Context, sessionID, path string) (io.ReadCloser, error)
	// DeleteFile removes a file from the session storage.
	DeleteFile(ctx context.Context, sessionID, path string) error
	// ListFiles returns all files in the session that match the given prefix.
	ListFiles(ctx context.Context, sessionID, prefix string) ([]string, error)
	// ClearSession removes all files associated with a session.
	ClearSession(ctx context.Context, sessionID string) error
	// GetSessionPath returns the filesystem path for a session's storage directory.
	GetSessionPath(sessionID string) string
}
