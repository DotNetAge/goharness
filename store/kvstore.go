package store

import "context"

// KVStore 定义会话级键值存储的接口。
// 实现为每个会话提供隔离的 KV 存储，支持
// 存储值上可选的 TTL（生存时间）。
type KVStore interface {
	// Set 在给定会话中存储一个键值对。
	// ttlSeconds 控制过期：0 = 不过期，>0 = N 秒后过期，<0 = 立即过期。
	Set(ctx context.Context, sessionID, key string, value []byte, ttlSeconds int) error
	// Get 从给定会话中获取键对应的值。
	// 如果键不存在或已过期，则返回 nil。
	Get(ctx context.Context, sessionID, key string) ([]byte, error)
	// Delete 从给定会话中删除一个键。
	Delete(ctx context.Context, sessionID, key string) error
	// ListKeys 返回给定会话中的所有键。
	ListKeys(ctx context.Context, sessionID string) ([]string, error)
	// ClearSession 删除会话关联的所有键。
	ClearSession(ctx context.Context, sessionID string) error
}
