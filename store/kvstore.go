package store

import "context"

type KVStore interface {
	Set(ctx context.Context, sessionID, key string, value []byte, ttlSeconds int) error
	Get(ctx context.Context, sessionID, key string) ([]byte, error)
	Delete(ctx context.Context, sessionID, key string) error
	ListKeys(ctx context.Context, sessionID string) ([]string, error)
	ClearSession(ctx context.Context, sessionID string) error
}
