package session

import (
	"context"
	"sync"

	"github.com/DotNetAge/goharness/memory"
)

// MemoryStore 定义了存储和检索会话记忆/压缩分块的接口。
// 实现可使用多种后端（Redis、数据库、RAGMemory 等）。
//
// StoreChunks 在压缩期间被调用，将压缩分块以 MemoryChunk 形式持久化。
// Retrieve 可在需要时用于加载历史上下文。
type MemoryStore interface {
	// StoreChunks 持久化与会话关联的记忆分块。
	StoreChunks(ctx context.Context, sessionID string, chunks []memory.MemoryChunk) error

	// Retrieve 加载会话的记忆分块，最多返回 `limit` 条最近的记录。
	Retrieve(ctx context.Context, query, sessionID string, limit int) ([]memory.MemoryChunk, error)
}

// inMemoryMemory 提供 MemoryStore 的内存实现，用于开发和测试。
type inMemoryMemory struct {
	mu   sync.RWMutex
	data map[string][]memory.MemoryChunk
}

// newInMemoryMemory 创建一个新的内存存储，初始化为空的数据 map。
func newInMemoryMemory() *inMemoryMemory {
	return &inMemoryMemory{data: make(map[string][]memory.MemoryChunk)}
}

// StoreChunks 将记忆分块按指定会话 ID 保存到存储中。
// 分块会追加到同一会话的现有记录之后。
func (m *inMemoryMemory) StoreChunks(_ context.Context, sessionID string, chunks []memory.MemoryChunk) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[sessionID] = append(m.data[sessionID], chunks...)
	return nil
}

// Retrieve 获取会话已存储的记忆分块，限制为最近的 `limit` 条记录。
func (m *inMemoryMemory) Retrieve(_ context.Context, query, sessionID string, limit int) ([]memory.MemoryChunk, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	all := m.data[sessionID]
	if len(all) == 0 {
		return nil, nil
	}
	if len(all) > limit {
		out := make([]memory.MemoryChunk, limit)
		copy(out, all[len(all)-limit:])
		return out, nil
	}
	out := make([]memory.MemoryChunk, len(all))
	copy(out, all)
	return out, nil
}
