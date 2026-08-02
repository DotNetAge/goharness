package session

import (
	"context"
	"errors"
	"time"

	"github.com/DotNetAge/goharness/memory"
)

// TokenUsage 记录单次 LLM 调用的 token 消耗。
// 字段名与标准 OpenAI 兼容 API 的 Usage 响应格式对齐。
// 如需带分组维度的持久化存储，使用 TokenUsageRecord。
type TokenUsage struct {
	Timestamp        time.Time `json:"timestamp" yaml:"timestamp"`
	PromptTokens     int       `json:"prompt_tokens" yaml:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens" yaml:"completion_tokens"`
	TotalTokens      int       `json:"total_tokens" yaml:"total_tokens"`

	// CachedTokens 是缓存的提示词 token 数
	// (prompt_tokens_details.cached_tokens / prompt_cache_hit_tokens)。
	CachedTokens int `json:"cached_tokens,omitempty" yaml:"cached_tokens"`

	// ReasoningTokens 是输出中的思考/推理 token 数
	// (completion_tokens_details.reasoning_tokens)。
	ReasoningTokens int `json:"reasoning_tokens,omitempty" yaml:"reasoning_tokens"`
}

// ActualTokens 返回实际净 token 消耗（排除缓存命中）。
func (u TokenUsage) ActualTokens() int {
	n := u.PromptTokens + u.CompletionTokens - u.CachedTokens
	if n < 0 {
		return 0
	}
	return n
}

// PricingUnit 定义每个模型的 token 定价（每百万 token）。
type PricingUnit struct {
	InputPricePer1M  float64
	OutputPricePer1M float64
}

// Cost 使用给定定价计算费用。
// 与 mindx/internal/core.CalculateCost 一致 — 标准定价算法。
// 缓存 token 减少可计费输入，而非单独计费。
func (u TokenUsage) Cost(p PricingUnit) float64 {
	netInput := u.PromptTokens - u.CachedTokens
	if netInput < 0 {
		netInput = 0
	}
	return float64(netInput)/1_000_000*p.InputPricePer1M +
		float64(u.CompletionTokens)/1_000_000*p.OutputPricePer1M
}

// SlideEvent 在上下文窗口滑出旧消息时发出。
// 包含被驱逐的消息，消费者（如 RAG/Memory）可以将其语义处理为长期知识。
type SlideEvent struct {
	SessionID string    `json:"session_id"`
	Slided    []Message `json:"slided"`
	Remaining int       `json:"remaining"`
	Timestamp int64     `json:"timestamp"`
}

// SlideHandler 是消费 slide 事件的回调类型。
// 实现可以将滑出的消息存储到 RAG 或其他长期存储中。
type SlideHandler func(ctx context.Context, event SlideEvent)

// SessionInfo 保存会话的元数据，用于 ListSessions 和 GetMeta。
// 包含对工具执行和提示词生成至关重要的目录上下文。
type SessionInfo struct {
	SessionID      string    `json:"session_id"`
	AgentName      string    `json:"agent_name,omitempty"`
	Sponsor        string    `json:"sponsor,omitempty"` // 创建此会话的智能体（空 = 用户发起）
	Title          string    `json:"title,omitempty"`   // 首条用户消息内容（用于会话列表显示）
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`            // 上次 meta.json 保存时间
	LastActivityAt time.Time `json:"last_activity_at"`      // 上次消息活动时间戳
	ProjectDir     string    `json:"project_dir,omitempty"` // 会话创建时的工作目录
	SessionDir     string    `json:"session_dir,omitempty"` // 会话沙箱目录（由 Store 管理）
	MessageCount   int       `json:"message_count"`         // 会话中的总消息数
	Cursor         int       `json:"cursor"`                // 压缩游标位置（0 = 未压缩）
	Messages       []Message `json:"messages,omitempty"`
	ModifyFiles    []string  `json:"modify_files,omitempty"` // 追踪的已修改文件路径
}

// Summarizer 定义了摘要器接口。
// 将消息列表浓缩为多个 MemoryChunk，每个包含 Title/Summary/Content 三段式结构与标签。
type Summarizer interface {
	// Summarize 将消息列表摘要为多个记忆片。
	// 返回的 MemoryChunk 采用三段式：Title（导航标题）、Summary（核心结论）、Content（分条细节）。
	// AgentName、SessionID、Timestamp 由调用方填充。
	Summarize(ctx context.Context, messages []Message) ([]memory.MemoryChunk, error)
}

type SessionStore interface {
	Append(ctx context.Context, sessionID string, agentName string, sponsor string, message Message) error
	Get(ctx context.Context, sessionID string) ([]Message, error)
	CurrentContext(ctx context.Context, agentName string, maxTokens int64) ([]Message, error)
	Delete(ctx context.Context, timestamp int64, sessionID string) error
	Clear(ctx context.Context, sessionID string) error
	SetSlideHandler(handler SlideHandler)
	Close() error
	ListSessions(ctx context.Context) ([]SessionInfo, error)
	Create(ctx context.Context, agentName string, opts ...SessionOption) (*SessionInfo, error)
	GetMeta(ctx context.Context, sessionID string) (*SessionInfo, error)
	ResolveSessionDir(sessionID string) (string, error)
	DeleteSession(ctx context.Context, sessionID string) error
	GetCursor(ctx context.Context, sessionID string) (int, error)
	SetCursor(ctx context.Context, sessionID string, cursor int) error

	// SaveModifyFiles 持久化文件修改追踪
	SaveModifyFiles(sessionID string, files []string) error
	GetModifyFiles(sessionID string) ([]string, error)

	// UpdateMessages 持久化对现有消息的修改（如 MicroCompact 对 Compacted 字段的更改）。
	// 接收当前游标和完整消息列表。存储原子替换会话的现有消息。
	UpdateMessages(ctx context.Context, sessionID string, cursor int, messages []Message) error

	// Truncate 移除 keepCount 之后的消息，只保留前 keepCount 条消息。
	Truncate(ctx context.Context, sessionID string, keepCount int) error
}

// SessionOption 是会话创建时的函数式选项。
type SessionOption func(*SessionInfo)

// WithProjectDirOption 为新会话信息设置项目工作目录。
// 如果未提供，实现应使用 os.Getwd() 作为默认值。
func WithProjectDirOption(dir string) SessionOption {
	return func(s *SessionInfo) {
		s.ProjectDir = dir
	}
}

// WithSponsorOption 为新会话信息设置发起智能体。
// Sponsor 标识创建/发起此会话的智能体。
// 空值表示用户发起。
func WithSponsorOption(sponsor string) SessionOption {
	return func(s *SessionInfo) {
		s.Sponsor = sponsor
	}
}

// NoopSlideHandler 是一个空操作的 SlideHandler，用于不需要它的实现。
func NoopSlideHandler(_ context.Context, _ SlideEvent) {}

var ErrSessionNotFound = errors.New("未找到对应角色的会话")
