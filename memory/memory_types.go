// Package memory provides interfaces and types for AI agent memory systems.
// It supports both session-scoped memory (ephemeral, conversation-bound) and
// long-term memory (persistent knowledge storage) with configurable retrieval options.
package memory

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Filter keys for memory retrieval scoping.
// These are used as Query.AddFilter keys to scope retrieval to specific agents/sessions.
const (
	FilterKeyAgentName  = "agent_name"
	FilterKeySessionID  = "session_id"
	FilterKeyProjectDir = "project_dir"
)

// MemoryChunk represents a single memory piece with full metadata.
// It is the core data structure for the memory system, distinct from
// the knowledge base (graph) storage.
type MemoryChunk struct {
	ID         string    `json:"id"`
	Title      string    `json:"title,omitempty"`
	Summary    string    `json:"summary"`
	Content    string    `json:"content"`
	AgentName  string    `json:"agent_name"`
	SessionID  string    `json:"session_id"`
	ProjectDir string    `json:"project_dir,omitempty"`
	Tags       []string  `json:"tags"`
	Timestamp  time.Time `json:"timestamp"`
}

// ErrMemoryNotFound is returned when a requested memory record doesn't exist.
var ErrMemoryNotFound = errors.New("记忆未找到")

// ErrMemoryStorage is returned when a memory storage operation fails.
var ErrMemoryStorage = errors.New("记忆存储失败")

// ErrMemoryRetrieval is returned when a memory retrieval operation fails.
var ErrMemoryRetrieval = errors.New("记忆检索失败")

// MemoryType defines the type of memory for retrieval filtering.
type MemoryType int

const (
	// MemoryTypeSession represents ephemeral session-scoped memory.
	// Cleared when the session ends.
	MemoryTypeSession MemoryType = iota

	// MemoryTypeLongTerm represents persistent long-term knowledge.
	// Survives across sessions.
	MemoryTypeLongTerm
)

// Memory defines the interface for memory storage and retrieval operations.
type Memory interface {
	// Retrieve searches for memory chunks matching the query with optional filters.
	Retrieve(ctx context.Context, query string, opts ...RetrieveOption) ([]MemoryChunk, error)
	// Store persists a new memory chunk and returns its ID.
	Store(ctx context.Context, chunk MemoryChunk) (string, error)
	// Delete removes a memory chunk by ID.
	Delete(ctx context.Context, id string) error
}

// LatestRetriever 是可选接口，用于按时间倒序取最新记忆（不依赖向量检索）。
// 实现此接口的 Memory 实现可以支持 memmache.md 中"记忆缓冲区固定取最新 N 条"的需求。
// 未实现此接口的 Memory 实现将被 MemoryThoughtHook 跳过时间倒序注入。
type LatestRetriever interface {
	RetrieveLatest(ctx context.Context, agentName, projectDir string, limit int) ([]MemoryChunk, error)
}

// SessionRetriever 是可选接口，用于按 sessionID 检索记忆（无视 agentName / projectDir 过滤）。
// 作为 LatestRetriever 的兜底：当按 agent+project 过滤空结果时，可用 sessionID 捞回。
type SessionRetriever interface {
	RetrieveBySession(ctx context.Context, sessionID string, limit int) ([]MemoryChunk, error)
}

// DefaultRetrieveConfig returns the default configuration for memory retrieval.
func DefaultRetrieveConfig() RetrieveConfig {
	return RetrieveConfig{Limit: 5}
}

// FormatMemoryRecords formats memory chunks into a human-readable string
// suitable for inclusion in AI prompts.
// Format: - [时间] [标题] - [内容] 。标签:[tag1, tag2, tag3]
func FormatMemoryRecords(chunks []MemoryChunk) string {
	if len(chunks) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, c := range chunks {
		// 时间
		sb.WriteString("- [")
		if !c.Timestamp.IsZero() {
			sb.WriteString(c.Timestamp.Format("2006-01-02 15:04"))
		}
		sb.WriteString("] ")
		// 标题：优先 Title（三段式导航标题），回退 Summary（旧数据兼容）
		if c.Title != "" {
			sb.WriteString(c.Title)
		}

		// 内容
		content := strings.TrimSpace(c.Content)
		listMode := false
		if content != "" {
			if isMarkdownList(content) {
				// Markdown 有序/无序列表：逐行作为子列表缩进两个空格
				listMode = true
				sb.WriteString("\n")
				for _, line := range strings.Split(content, "\n") {
					if line = strings.TrimSpace(line); line == "" {
						continue
					}
					sb.WriteString("  ")
					sb.WriteString(line)
					sb.WriteString("\n")
				}
			} else {
				// 普通文本：在内容前加 " - " 直接输出，多行续行缩进对齐
				sb.WriteString(" - ")
				sb.WriteString(strings.ReplaceAll(content, "\n", "\n  "))
			}
		}
		// 标签
		if len(c.Tags) > 0 {
			if listMode {
				// 列表模式：标签作为独立缩进行，与子列表项对齐
				sb.WriteString("  ")
			}
			sb.WriteString(" 。标签:[")
			for i, tag := range c.Tags {
				if i > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(tag)
			}
			sb.WriteString("]")
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String())
}

// isMarkdownList 判断内容是否为 Markdown 有序/无序列表：
// 所有非空行均须为列表项（无序：-/*/+ 开头；有序：数字后跟 . 或 )）。
func isMarkdownList(content string) bool {
	lines := strings.Split(content, "\n")
	nonEmpty := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonEmpty++
		if !isMarkdownListLine(line) {
			return false
		}
	}
	return nonEmpty > 0
}

// isMarkdownListLine 判断单行是否为 Markdown 列表项。
func isMarkdownListLine(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	switch trimmed[0] {
	case '-', '*', '+':
		return true
	}
	// 有序列表：数字后跟 "." 或 ")"
	i := 0
	for i < len(trimmed) && trimmed[i] >= '0' && trimmed[i] <= '9' {
		i++
	}
	return i > 0 && i < len(trimmed) && (trimmed[i] == '.' || trimmed[i] == ')')
}
