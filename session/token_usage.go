package session

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TokenUsageRecord 表示单次 LLM API 调用的 token 消耗，带有分组维度。
// 它遵循只写策略：插入一次后不再修改。
type TokenUsageRecord struct {
	// ID 是此记录的唯一标识符。
	ID string `json:"id"`

	// --- 分组维度 ---
	SessionID      string `json:"session_id"`
	ConversationID string `json:"conversation_id"`
	ModelName      string `json:"model_name"`
	ProviderName   string `json:"provider_name"`
	AgentName      string `json:"agent_name"`

	// --- Token 计数 ---
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
	TotalTokens      int `json:"total_tokens"`

	// Timestamp 是 LLM API 调用完成的时间。
	Timestamp time.Time `json:"timestamp"`
}

// TokenUsageFilter 定义检索使用记录的查询维度。
// 空/零值字段表示"任意"（该维度不做过滤）。
type TokenUsageFilter struct {
	SessionID      string
	ConversationID string
	ModelName      string
	ProviderName   string
	AgentName      string
	Since          time.Time
	Until          time.Time
}

// TokenUsageStore 是 token 使用记录的存储接口。
//
// goharness 是一个开发框架 —— 存储实现从外部注入。
// 实现可使用 SQLite、PostgreSQL 或任何其他后端。
// 框架默认提供一个内存实现作为后备。
//
// 设计原则：
//   - 只写：记录仅支持 INSERT，永不更新或删除。
//   - 多维度查询：使用 TokenUsageFilter 进行灵活聚合。
type TokenUsageStore interface {
	// Append 写入单条使用记录。只写：记录永不修改或删除。
	Append(ctx context.Context, record TokenUsageRecord) error

	// Query 检索匹配给定过滤器的使用记录。
	// 按插入顺序返回所有匹配记录。
	Query(ctx context.Context, filter TokenUsageFilter) ([]TokenUsageRecord, error)

	// Close 释放存储持有的资源。
	Close() error
}

// InMemoryTokenUsageStore 提供 TokenUsageStore 的内存实现。
// 当未注入外部存储时，这是默认的后备实现。
// 支持并发安全访问。
type InMemoryTokenUsageStore struct {
	mu      sync.RWMutex
	records []TokenUsageRecord
}

// NewInMemoryTokenUsageStore 创建一个新的 InMemoryTokenUsageStore。
func NewInMemoryTokenUsageStore() *InMemoryTokenUsageStore {
	return &InMemoryTokenUsageStore{}
}

// Append 向内存存储追加一条使用记录。
func (s *InMemoryTokenUsageStore) Append(_ context.Context, record TokenUsageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = append(s.records, record)
	return nil
}

// Query 检索匹配给定过滤器的使用记录。
func (s *InMemoryTokenUsageStore) Query(_ context.Context, filter TokenUsageFilter) ([]TokenUsageRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []TokenUsageRecord
	for _, r := range s.records {
		if filter.SessionID != "" && r.SessionID != filter.SessionID {
			continue
		}
		if filter.ConversationID != "" && r.ConversationID != filter.ConversationID {
			continue
		}
		if filter.ModelName != "" && r.ModelName != filter.ModelName {
			continue
		}
		if filter.ProviderName != "" && r.ProviderName != filter.ProviderName {
			continue
		}
		if filter.AgentName != "" && r.AgentName != filter.AgentName {
			continue
		}
		if !filter.Since.IsZero() && r.Timestamp.Before(filter.Since) {
			continue
		}
		if !filter.Until.IsZero() && r.Timestamp.After(filter.Until) {
			continue
		}
		result = append(result, r)
	}

	out := make([]TokenUsageRecord, len(result))
	copy(out, result)
	return out, nil
}

// Close 对于内存存储是空操作。
func (s *InMemoryTokenUsageStore) Close() error {
	return nil
}

// NoopTokenUsageStore 是 TokenUsageStore 的空操作实现。
// 在禁用 token 使用追踪时使用。
type NoopTokenUsageStore struct{}

// NewNoopTokenUsageStore 创建一个新的 NoopTokenUsageStore。
func NewNoopTokenUsageStore() *NoopTokenUsageStore {
	return &NoopTokenUsageStore{}
}

// Append 是空操作。
func (s *NoopTokenUsageStore) Append(_ context.Context, _ TokenUsageRecord) error {
	return nil
}

// Query 返回空结果。
func (s *NoopTokenUsageStore) Query(_ context.Context, _ TokenUsageFilter) ([]TokenUsageRecord, error) {
	return nil, nil
}

// Close 是空操作。
func (s *NoopTokenUsageStore) Close() error {
	return nil
}

// NewRecordID 为 token 使用记录生成唯一 ID。
// 格式："tur_<nanoseconds>"（tur = token usage record）。
func NewRecordID() string {
	return fmt.Sprintf("tur_%d", time.Now().UnixNano())
}
