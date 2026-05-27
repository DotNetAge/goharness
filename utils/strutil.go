package utils

import "strings"

// StripMarkdownCodeBlock removes markdown code block markers (```...```) from content.
// If the content does not start with ```, it is returned as-is (trimmed).
// Handles both ```json and bare ``` opening markers.
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
