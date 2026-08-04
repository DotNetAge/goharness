package session

import "context"

// ── 存储层操作 ──────────────────────────────────────────────────────────
// 这些函数封装了 SessionStore 方法，使外部代码无需直接调用
// SessionStore 方法。所有会话操作都通过 session 包进行。

// ListSessions 从存储中返回所有会话。
func ListSessions(ctx context.Context, store SessionStore) ([]SessionInfo, error) {
	return store.ListSessions(ctx)
}

// CreateSession 在存储中创建一个新会话，使用给定的智能体名称和选项。
func CreateSession(ctx context.Context, store SessionStore, agentName string, opts ...SessionOption) (*SessionInfo, error) {
	return store.Create(ctx, agentName, opts...)
}

// DeleteSession 从存储中移除一个会话。
func DeleteSession(ctx context.Context, store SessionStore, sessionID string) error {
	return store.DeleteSession(ctx, sessionID)
}

// GetSessionMeta 从存储中返回会话元数据。
func GetSessionMeta(ctx context.Context, store SessionStore, sessionID string) (*SessionInfo, error) {
	return store.GetMeta(ctx, sessionID)
}
