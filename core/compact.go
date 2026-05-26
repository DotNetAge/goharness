package core

import (
	"encoding/json"
	"fmt"
)

// BudgetCompact performs a quick, non-LLM compaction by truncating large
// observation/tool-result messages while preserving small messages intact.
// Unlike MicroCompact (which removes read-only tool results by round),
// BudgetCompact trims message content to fit within a token budget.
func BudgetCompact(messages []Message, estimateFn func(string) int, targetTokens int64) []Message {
	if estimateFn == nil {
		estimateFn = func(s string) int { return len(s)/4 + 1 }
	}
	if len(messages) <= 2 {
		return messages
	}

	var totalTokens int64
	for _, m := range messages {
		totalTokens += int64(estimateFn(m.Content))
	}

	if totalTokens <= targetTokens {
		return messages
	}

	remaining := targetTokens
	result := make([]Message, 0, len(messages))

	type msgToken struct {
		msg    Message
		tokens int64
	}
	items := make([]msgToken, len(messages))
	for i, m := range messages {
		items[i] = msgToken{msg: m, tokens: int64(estimateFn(m.Content))}
	}

	for i := len(items) - 1; i >= 0; i-- {
		if items[i].tokens <= remaining {
			result = append(result, items[i].msg)
			remaining -= items[i].tokens
		} else if remaining > 100 {
			charsBudget := remaining * 3
			runes := []rune(items[i].msg.Content)
			if len(runes) > int(charsBudget) {
				truncated := string(runes[:int(charsBudget)]) + "\n... [compacted] ..."
				result = append(result, Message{
					Role:      items[i].msg.Role,
					Content:   truncated,
					Timestamp: items[i].msg.Timestamp,
				})
				remaining = 0
			} else {
				result = append(result, items[i].msg)
				remaining -= items[i].tokens
			}
		}
	}

	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}

	return result
}

// TrimJSONResult attempts to trim a JSON result by removing large array elements
// or string values while keeping the structure intact. Useful for tool results
// that return structured data (e.g., search results, file listings).
func TrimJSONResult(jsonStr string, maxChars int) string {
	if len(jsonStr) <= maxChars {
		return jsonStr
	}

	var data any
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		runes := []rune(jsonStr)
		if len(runes) > maxChars {
			return string(runes[:maxChars]) + "\n... [truncated] ..."
		}
		return jsonStr
	}

	trimmed := trimJSONValue(data, maxChars)
	result, _ := json.Marshal(trimmed)
	if len(result) > maxChars {
		result = result[:maxChars]
		result = append(result, []byte("\n... [truncated] ...")...)
	}
	return string(result)
}

func trimJSONValue(v any, maxChars int) any {
	switch val := v.(type) {
	case map[string]any:
		result := make(map[string]any)
		currentSize := 0
		for k, v := range val {
			if currentSize > maxChars/2 {
				result[k] = fmt.Sprintf("[trimmed: %d remaining keys skipped]", len(val)-len(result))
				break
			}
			trimmed := trimJSONValue(v, maxChars/2)
			b, _ := json.Marshal(trimmed)
			currentSize += len(b)
			result[k] = trimmed
		}
		return result
	case []any:
		if len(val) > 20 {
			kept := make([]any, 0, 15)
			for i := 0; i < 10 && i < len(val); i++ {
				kept = append(kept, trimJSONValue(val[i], maxChars/15))
			}
			kept = append(kept, fmt.Sprintf("[%d items omitted]", len(val)-15))
			for i := len(val) - 5; i < len(val); i++ {
				kept = append(kept, trimJSONValue(val[i], maxChars/15))
			}
			return kept
		}
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = trimJSONValue(item, maxChars/len(val))
		}
		return result
	case string:
		runes := []rune(val)
		if len(runes) > 500 {
			return string(runes[:500]) + "... [trimmed] ..."
		}
		return val
	default:
		return v
	}
}
