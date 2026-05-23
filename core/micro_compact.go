package core

import "strings"

// readOnlyToolNames lists tools whose results are safe to compact away
// after the LLM has consumed them. These tools have no side effects —
// re-executing them returns the same result.
var readOnlyToolNames = map[string]bool{
	"Read":      true,
	"Grep":      true,
	"Glob":      true,
	"WebSearch": true,
	"WebFetch":  true,
	"Skill":     true,
	"AskUser":   true,
}

// IsReadOnlyTool checks whether a tool name is in the read-only set.
func IsReadOnlyTool(name string) bool {
	return readOnlyToolNames[name]
}

// MicroCompact removes old read-only tool results from a message list,
// keeping only the most recent `keepRecent` assistant-turns' worth.
//
// Strategy:
//   - Parse messages into rounds delimited by assistant messages.
//   - Keep the most recent `keepRecent` rounds intact.
//   - For earlier rounds, remove tool-role messages whose content
//     starts with a read-only tool name (e.g. "[Read]").
//   - Corresponding ToolCall entries in assistant messages are also
//     stripped to prevent orphaned tool_call_ids (required by strict
//     APIs like DeepSeek that validate tool_call_id matching).
//   - Non-read-only tool results (Bash, Write, Edit) are always kept.
//   - User and assistant messages are always kept.
//
// This creates a copy; the original slice is not modified.
//
// Parameters:
//   - messages: the full conversation history
//   - keepRecent: number of recent assistant-turns to preserve fully (minimum 1)
//
// The returned slice may share underlying array elements with the input
// for unchanged messages, but the result itself is a new slice header.
// ToolCall slices in the result are freshly allocated to avoid aliasing.
func MicroCompact(messages []Message, keepRecent int) []Message {
	if keepRecent < 1 {
		keepRecent = 1
	}

	// Find the positions of all assistant messages.
	// Each assistant message marks the start of a new "round".
	var asstIdxs []int
	for i, m := range messages {
		if m.Role == "assistant" {
			asstIdxs = append(asstIdxs, i)
		}
	}

	// Determine which assistant rounds to keep.
	// Everything from (and including) the N-th-from-last assistant
	// onwards is fully preserved.
	keepFromIdx := 0
	if len(asstIdxs) > keepRecent {
		keepFromIdx = asstIdxs[len(asstIdxs)-keepRecent]
	}

	// First pass: build result and collect removed tool_call_ids.
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

		if !isReadOnlyToolResult(m.Content) {
			result = append(result, m)
			continue
		}

		if m.ToolCallID != "" {
			removedToolCallIDs[m.ToolCallID] = true
		}
	}

	// Second pass: strip orphaned ToolCalls from assistant messages
	// in the compaction zone.
	if len(removedToolCallIDs) > 0 {
		for i := range result {
			if i >= keepFromIdx {
				break
			}
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
					Role:      result[i].Role,
					Content:   result[i].Content,
					Timestamp: result[i].Timestamp,
					ToolCalls: kept,
				}
			}
		}
	}

	return result
}

// isReadOnlyToolResult checks whether a tool result string was produced
// by a read-only tool, by examining the "[ToolName]" prefix.
func isReadOnlyToolResult(content string) bool {
	if len(content) < 3 || content[0] != '[' {
		return false
	}
	end := strings.IndexByte(content, ']')
	if end == -1 || end == 1 {
		return false
	}
	name := content[1:end]
	return readOnlyToolNames[name]
}
