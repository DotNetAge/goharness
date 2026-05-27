package store

import (
	"context"
	"io"
)

type FileStore interface {
	WriteFile(ctx context.Context, sessionID, path string, content io.Reader) error
	ReadFile(ctx context.Context, sessionID, path string) (io.ReadCloser, error)
	DeleteFile(ctx context.Context, sessionID, path string) error
	ListFiles(ctx context.Context, sessionID, prefix string) ([]string, error)
	ClearSession(ctx context.Context, sessionID string) error
	GetSessionPath(sessionID string) string
}
