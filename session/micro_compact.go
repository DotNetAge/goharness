package session

import "strings"

var readOnlyToolNames = map[string]bool{
	"Read":      true,
	"Grep":      true,
	"Glob":      true,
	"WebSearch": true,
	"WebFetch":  true,
	"Skill":     true,
	"AskUser":   true,
}

func IsReadOnlyTool(name string) bool {
	return readOnlyToolNames[name]
}

func MicroCompact(messages []Message, keepRecent int) []Message {
	if keepRecent < 1 {
		keepRecent = 1
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

		if !isReadOnlyToolResult(m.Content) {
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
