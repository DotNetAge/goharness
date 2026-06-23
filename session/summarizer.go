package session

import (
	"context"

	"github.com/DotNetAge/goharness/memory"
)

// Summarizer 定义了摘要器接口。
// 将消息列表浓缩为多个 MemoryChunk，每个包含摘要、内容和标签。
type Summarizer interface {
	// Summarize 将消息列表摘要为多个记忆片。
	// 返回的 MemoryChunk 包含 Summary（摘要）、Content（原文精华）和 Tags（标签）。
	// AgentName、SessionID、Timestamp 由调用方填充。
	Summarize(ctx context.Context, messages []Message) ([]memory.MemoryChunk, error)
}
