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

	// Build a lookup from tool_call_id -> tool name using the assistant
	// messages' ToolCalls lists. We prefer this over parsing the tool
	// message's Content, because the runtime may not always prefix the
	// result with "[ToolName]" (e.g. when the tool returns a raw string).
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

		// Resolve the tool name either from the assistant's ToolCalls map
		// (preferred) or by falling back to the content prefix for legacy
		// session files that predate the ToolName field.
		name := toolNameByID[m.ToolCallID]
		if name == "" {
			name = parseToolNameFromContent(m.Content)
		}
		if name != "" && !IsReadOnlyToolResultName(name) {
			result = append(result, m)
			continue
		}
		// If we can't determine the name, keep the tool message rather than
		// drop it blindly — losing it would leave the assistant.ToolCalls
		// out of sync.
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

func isReadOnlyToolResult(content string) bool {
	return IsReadOnlyToolResultName(parseToolNameFromContent(content))
}

func IsReadOnlyToolResultName(name string) bool {
	return readOnlyToolNames[name]
}
