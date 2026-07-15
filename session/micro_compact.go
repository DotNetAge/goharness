package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// processContent computes SHA256 (first 32 hex chars, 128 bit) and DeepSeek-based
// token estimate in a single pass over the content.
//
// Token estimation uses the DeepSeek official formula:
//   - ASCII/English characters ≈ 0.3 token each
//   - CJK/full-width characters ≈ 0.6 token each
func processContent(content string) (sha32 string, tokens int64) {
	h := sha256.New()
	var t float64
	for _, r := range content {
		h.Write([]byte(string(r)))
		if r <= 0x7F {
			t += 0.3
		} else {
			t += 0.6
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:32], int64(t) + 1
}

// estimateWindowTokensV2 estimates the token count of the given messages as they
// appear in the active context window.
//
// For assistant messages with Usage data, only CompletionTokens (+ ReasoningTokens)
// are counted. TotalTokens must NOT be used here because it includes the entire
// prompt history sent with that request, which would repeatedly count earlier
// messages already present in the window.
//
// For compacted messages (Compacted != ""), uses the placeholder size (~20 tokens).
// For all other messages, falls back to the DeepSeek character-level estimation.
func estimateWindowTokensV2(msgs []Message) int64 {
	var total int64
	for _, m := range msgs {
		if m.Compacted != "" {
			total += 20 // placeholder "…" ≈ 20 tokens
		} else if m.Role == "assistant" && m.Usage != nil && (m.Usage.CompletionTokens > 0 || m.Usage.ReasoningTokens > 0) {
			total += int64(m.Usage.CompletionTokens + m.Usage.ReasoningTokens)
		} else {
			_, tokens := processContent(m.Content)
			total += tokens
		}
	}
	return total
}

// BuildToolNameByID builds a map from tool_call_id → tool name by scanning
// assistant messages' ToolCalls lists.
func BuildToolNameByID(msgs []Message) map[string]string {
	m := make(map[string]string)
	for _, msg := range msgs {
		if msg.Role != "assistant" {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" && tc.Name != "" {
				m[tc.ID] = tc.Name
			}
		}
	}
	return m
}

// RenderCompactedPlaceholder returns the single-line placeholder that replaces
// the content of a compressed tool message in the LLM context.
//
// Format: [已压缩] 工具: {tool} | {n} tokens | 路径: {path}
func RenderCompactedPlaceholder(msg Message, toolNameByID map[string]string) string {
	var meta CompactedMeta
	if err := json.Unmarshal([]byte(msg.Compacted), &meta); err != nil {
		// Corrupted compacted data → show raw value as fallback
		return msg.Compacted
	}

	toolName := meta.ToolName
	if name, ok := toolNameByID[msg.ToolCallID]; ok && name != "" {
		toolName = name
	}

	return fmt.Sprintf("[已压缩] 工具: %s | %d tokens | 路径: %s",
		toolName, meta.TokenCount, meta.Path)
}

// stripDuplicateToolMessages removes adjacent tool messages with identical content.
// Only operates on role="tool" messages to avoid breaking assistant-tool pairings.
// Returns the deduplicated slice and the set of orphaned ToolCallIDs that were removed.
func stripDuplicateToolMessages(msgs []Message) ([]Message, map[string]bool) {
	orphaned := make(map[string]bool)
	if len(msgs) < 2 {
		return msgs, orphaned
	}
	out := make([]Message, 0, len(msgs))
	out = append(out, msgs[0])
	for i := 1; i < len(msgs); i++ {
		prev := out[len(out)-1]
		curr := msgs[i]
		if curr.Role == "tool" && prev.Role == "tool" && curr.Content == prev.Content {
			// Mark the skipped message's ToolCallID as orphaned so the
			// corresponding assistant ToolCall entry can be cleaned up.
			if curr.ToolCallID != "" {
				orphaned[curr.ToolCallID] = true
			}
			continue
		}
		out = append(out, curr)
	}
	return out, orphaned
}

// ── Session method: TryMicroCompact ──────────────────────────────────────

const (
	microCompactTriggerRatio  = 0.45 // start compressing when window >= 45% maxWindowSize
	microCompactTargetRatio   = 0.40 // stop compressing when window <= 40% maxWindowSize
	microCompactPositionStart = 0.25 // only compress messages in [25%, 65%] position range
	microCompactPositionEnd   = 0.65
	microCompactMinTokens     = 500 // skip short messages (not worth compressing)
)

// TryMicroCompact checks whether the session's active window exceeds the
// MicroCompact trigger threshold (45% of maxWindowSize). If so, it compresses
// eligible tool messages in the 25%-65% position range until the window drops
// below 40%, then persists the session.
//
// Returns true if compression was performed and the session was saved.
func (s *Session) TryMicroCompact(sessionDir string) bool {
	if s.maxWindowSize <= 0 {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	window := s.messages[s.cursor:] // 偏移量模型：当前窗口 = messages[cursor:]
	if len(window) == 0 {
		return false
	}

	// Step 1: Check trigger threshold
	windowTokens := estimateWindowTokensV2(window)
	triggerTokens := int64(float64(s.maxWindowSize) * microCompactTriggerRatio)
	if windowTokens < triggerTokens {
		return false
	}

	// Fire micro-compact start handler
	if s.microCompactStartHandler != nil {
		s.microCompactStartHandler(windowTokens, s.maxWindowSize)
	}

	// Step 2: Strip duplicate tool messages first (cheap, reduces noise)
	deduped, orphanedIDs := stripDuplicateToolMessages(window)
	dedupCount := len(window) - len(deduped)
	hadDedup := dedupCount > 0
	if hadDedup {
		// 偏移量模型：保留历史分区 messages[:cursor]，用去重结果替换活跃窗口
		newMessages := make([]Message, 0, s.cursor+len(deduped))
		newMessages = append(newMessages, s.messages[:s.cursor]...)
		newMessages = append(newMessages, deduped...)
		s.messages = newMessages
		// cursor 不变 —— 仍指向历史分区边界
		// Clean up orphaned ToolCalls from removed duplicate tool messages
		if len(orphanedIDs) > 0 {
			s.removeOrphanedToolCalls(s.cursor, orphanedIDs)
		}
	}
	// Refresh window from s.messages so candidate pointers reference the
	// canonical slice (important after dedup replaced the message list).
	window = s.messages[s.cursor:]

	// Step 3: Recalculate after dedup
	windowTokens = estimateWindowTokensV2(window)
	if windowTokens < triggerTokens {
		return hadDedup
	}

	// Step 4: Build tool name lookup
	toolNameByID := BuildToolNameByID(window)

	// Step 5: Find eligible candidates — role="tool", 25%-65% position,
	// content > 500 tokens, compacted empty
	type candidate struct {
		idx       int
		msg       *Message
		timestamp int64
	}
	positionStart := int(float64(len(window)) * microCompactPositionStart)
	positionEnd := int(float64(len(window)) * microCompactPositionEnd)
	targetTokens := int64(float64(s.maxWindowSize) * microCompactTargetRatio)

	var candidates []candidate
	for i := positionStart; i < positionEnd && i < len(window); i++ {
		m := &window[i]
		if m.Role != "tool" || m.Compacted != "" {
			continue
		}
		_, tokens := processContent(m.Content)
		if tokens <= microCompactMinTokens {
			continue
		}
		candidates = append(candidates, candidate{idx: i, msg: m, timestamp: m.Timestamp})
	}

	if len(candidates) == 0 {
		return hadDedup
	}

	// Sort by timestamp ascending (oldest first)
	sort.Slice(candidates, func(a, b int) bool {
		return candidates[a].timestamp < candidates[b].timestamp
	})

	// Step 6: Compress candidates one by one until below target threshold
	var compressed int
	for _, c := range candidates {
		if windowTokens <= targetTokens {
			break
		}

		sha32, tokenCount := processContent(c.msg.Content)

		// Write cache file
		if sessionDir != "" {
			cacheDir := filepath.Join(sessionDir, "microcompact")
			if err := os.MkdirAll(cacheDir, 0755); err == nil {
				cachePath := filepath.Join(cacheDir, sha32+".md")
				if err := os.WriteFile(cachePath, []byte(c.msg.Content), 0644); err == nil {
					meta := CompactedMeta{
						Path:       cachePath,
						ToolName:   toolNameByID[c.msg.ToolCallID],
						TokenCount: tokenCount,
					}
					data, _ := json.Marshal(meta)
					c.msg.Compacted = string(data)
					// Compressed message now contributes ~20 tokens (placeholder)
					// instead of tokenCount. Subtract the difference.
					windowTokens -= (tokenCount - 20)
					compressed++
				}
			}
		}
	}

	// Step 7: Persist compacted changes + cursor to store
	if compressed > 0 || hadDedup {
		if s.store != nil {
			if err := s.store.UpdateMessages(context.Background(), s.id, s.cursor, s.messages); err != nil {
				// Log but don't fail — compacted state is still correct in memory
			}
		}
		if s.microCompactDoneHandler != nil {
			s.microCompactDoneHandler(compressed, dedupCount, windowTokens)
		}
		return true
	}

	return false
}

// removeOrphanedToolCalls removes ToolCall entries from assistant messages
// whose tool_call_id is in the orphaned set. This is called after dedup to
// prevent "insufficient tool messages" errors from orphaned ToolCall entries.
func (s *Session) removeOrphanedToolCalls(cursor int, orphanedIDs map[string]bool) {
	for i := cursor; i < len(s.messages); i++ {
		m := &s.messages[i]
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		kept := make([]ToolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			if !orphanedIDs[tc.ID] {
				kept = append(kept, tc)
			}
		}
		if len(kept) != len(m.ToolCalls) {
			m.ToolCalls = kept
		}
	}
}

// ── Legacy helpers: kept for Compact() and backward compatibility ─────────

// IsReadOnlyTool returns whether the given tool name is a read-only tool.
// Kept for backward compatibility with existing callers.
func IsReadOnlyTool(name string) bool {
	return name == "Read" || name == "Grep" || name == "Glob" ||
		name == "WebSearch" || name == "WebFetch" || name == "Skill" || name == "AskUser"
}

// MicroCompact is the legacy compression function. It is kept for backward
// compatibility with Session.Compact() which uses it on historical (out-of-window)
// messages. New code should use TryMicroCompact for in-window compression.
func MicroCompact(messages []Message, keepRecent int) []Message {
	if keepRecent < 1 {
		keepRecent = 1
	}

	// Build a lookup from tool_call_id -> tool name
	toolNameByID := make(map[string]string)
	for _, m := range messages {
		if m.Role != "assistant" {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.ID != "" && tc.Name != "" {
				toolNameByID[tc.ID] = tc.Name
			}
		}
	}

	var asstIdxs []int
	for i, m := range messages {
		if m.Role == "assistant" {
			asstIdxs = append(asstIdxs, i)
		}
	}

	keepFromIdx := 0
	if len(asstIdxs) > keepRecent {
		keepFromIdx = asstIdxs[len(asstIdxs)-keepRecent]
	}

	removedToolCallIDs := make(map[string]bool)
	result := make([]Message, 0, len(messages))
	for i, m := range messages {
		if i >= keepFromIdx {
			result = append(result, m)
			continue
		}

		if m.Role != "tool" {
			result = append(result, m)
			continue
		}

		name := toolNameByID[m.ToolCallID]
		if name == "" {
			name = parseToolNameFromContent(m.Content)
		}
		if name != "" && !IsReadOnlyTool(name) {
			result = append(result, m)
			continue
		}
		if name == "" {
			result = append(result, m)
			continue
		}

		if m.ToolCallID != "" {
			removedToolCallIDs[m.ToolCallID] = true
		}
	}

	if len(removedToolCallIDs) > 0 {
		for i := range result {
			if result[i].Role != "assistant" || len(result[i].ToolCalls) == 0 {
				continue
			}
			kept := make([]ToolCall, 0, len(result[i].ToolCalls))
			for _, tc := range result[i].ToolCalls {
				if !removedToolCallIDs[tc.ID] {
					kept = append(kept, tc)
				}
			}
			if len(kept) != len(result[i].ToolCalls) {
				result[i] = Message{
					Role:             result[i].Role,
					Content:          result[i].Content,
					Compacted:        result[i].Compacted,
					ReasoningContent: result[i].ReasoningContent,
					Timestamp:        result[i].Timestamp,
					ToolCalls:        kept,
				}
			}
		}
	}

	return result
}

func parseToolNameFromContent(content string) string {
	if len(content) < 3 || content[0] != '[' {
		return ""
	}
	end := strings.IndexByte(content, ']')
	if end == -1 || end == 1 {
		return ""
	}
	return content[1:end]
}

func IsReadOnlyToolResultName(name string) bool {
	return IsReadOnlyTool(name)
}
