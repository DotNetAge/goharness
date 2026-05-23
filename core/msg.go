package core

import (
	"unicode"
)

// EstimateTokens is now backed by tiktoken BPE tokenizer (GPT-4o / o200k_base)
// with automatic fallback to character-class heuristics.
// See core/tiktoken.go for the full implementation.
//
// Deprecated: Use CountTokens for error-aware counting, or keep using
// EstimateTokens for backward-compatible zero-error behavior.

func isCJK(r rune) bool {
	return unicode.Is(unicode.Han, r) ||
		unicode.Is(unicode.Hiragana, r) ||
		unicode.Is(unicode.Katakana, r) ||
		unicode.Is(unicode.Hangul, r) ||
		(r >= 0x2E80 && r <= 0x2EFF) || // CJK Radicals Supplement
		(r >= 0x3000 && r <= 0x303F) || // CJK Symbols and Punctuation
		(r >= 0xFF00 && r <= 0xFFEF) // Fullwidth Forms
}

// ToolCall represents a tool invocation in an assistant message.
// Mirrors gochat/core.ToolCall for wire-format compatibility.
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded argument map
}

// Message represents a single message in a conversation.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	Timestamp  int64      `json:"timestamp"`
	ToolCallID string     `json:"tool_call_id,omitempty"` // tool call ID for role="tool" messages (required by strict APIs like DeepSeek)
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`   // tool invocations on role="assistant" messages (required by strict APIs like DeepSeek)
}
