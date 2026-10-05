package tools

import "strings"

// splitQueryTokens 将查询按中英文逗号拆分为关键词 token 列表。
// 长文本（>50 字符）视为自然语句不拆分；无逗号或拆分后无有效 token 时
// 返回仅含原始查询的单元素列表；过滤过短（≤1 字符）的 token。
// 供 WebSearch 等需要多关键词分别检索的工具复用。
func splitQueryTokens(query string) []string {
	// 长文本视为自然语句，不拆分
	if len(query) > 50 {
		return []string{query}
	}

	// 按中英文逗号拆分
	parts := strings.FieldsFunc(query, func(r rune) bool {
		return r == ',' || r == '，'
	})
	if len(parts) <= 1 {
		return []string{query}
	}

	// 过滤掉过短的词（<=1 字符）和首尾空白
	tokens := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 2 {
			tokens = append(tokens, p)
		}
	}
	if len(tokens) == 0 {
		return []string{query}
	}
	return tokens
}
