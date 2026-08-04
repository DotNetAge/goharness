package utils

import "strings"

// StripMarkdownCodeBlock 移除内容中的 Markdown 代码块标记（```...```）。
// 若内容不以 ``` 开头，则原样返回（仅去除首尾空白）。
// 同时处理 ```json 和裸 ``` 开头标记。
func StripMarkdownCodeBlock(content string) string {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "```") {
		return content
	}
	lines := strings.Split(content, "\n")
	var cleaned []string
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "```") {
			break
		}
		cleaned = append(cleaned, line)
	}
	return strings.TrimSpace(strings.Join(cleaned, "\n"))
}
