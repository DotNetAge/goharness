package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	previewMaxBytes   = 2000
	previewMinNewline = 1000  // 50% threshold for newline-boundary cut
	toolResultsDir    = "tool-results"
)

// DiskResultPersister persists large tool results to disk.
// Storage path: {sessionDir}/tool-results/{toolUseId}.txt
type DiskResultPersister struct {
	sessionDir string
}

func NewDiskResultPersister(sessionDir string) *DiskResultPersister {
	return &DiskResultPersister{sessionDir: sessionDir}
}

// Persist writes a large tool result to disk and returns a PersistedToolResult summary.
// Returns nil if the result is empty or below the persistence threshold (caller decides threshold).
func (d *DiskResultPersister) Persist(toolName, toolUseID, result string) *PersistedToolResult {
	charCount := len([]rune(result))
	if charCount <= 0 {
		return nil
	}
	if d.sessionDir == "" {
		return &PersistedToolResult{
			ToolName: toolName,
			FullSize: charCount,
			Preview:  buildPreview(result),
			FilePath: "",
		}
	}

	persistDir := filepath.Join(d.sessionDir, toolResultsDir)
	if err := os.MkdirAll(persistDir, 0755); err != nil {
		return &PersistedToolResult{
			ToolName: toolName,
			FullSize: charCount,
			Preview:  buildPreview(result),
			FilePath: "",
		}
	}

	filename := toolUseID + ".txt"
	filePath := filepath.Join(persistDir, filename)
	if err := os.WriteFile(filePath, []byte(result), 0644); err != nil {
		return &PersistedToolResult{
			ToolName: toolName,
			FullSize: charCount,
			Preview:  buildPreview(result),
			FilePath: "",
		}
	}

	return &PersistedToolResult{
		ToolName: toolName,
		FullSize: charCount,
		Preview:  buildPreview(result),
		FilePath: filePath,
	}
}

// PersistWithTag is a convenience method that persists and returns the replacement tag string.
// The second return value indicates whether the result was actually persisted (false = skipped).
func (d *DiskResultPersister) PersistWithTag(toolName, toolUseID, result string) (string, bool) {
	p := d.Persist(toolName, toolUseID, result)
	if p == nil {
		return "", false
	}
	return PersistedResultTag(p), true
}

// PersistedResultTag builds the Claude Code style <persisted-output> tag for a persisted result.
func PersistedResultTag(p *PersistedToolResult) string {
	if p == nil {
		return ""
	}
	if p.FilePath == "" {
		return fmt.Sprintf("[Result from %s: %d chars, preview truncated]\n%s\n[End]",
			p.ToolName, p.FullSize, p.Preview)
	}
	var b strings.Builder
	b.WriteString("<persisted-output>\n")
	b.WriteString(fmt.Sprintf("Output too large (%d KB). Full output saved to: %s\n\n",
		(p.FullSize+1023)/1024, p.FilePath))
	b.WriteString("Preview (first 2000 bytes):\n")
	b.WriteString(p.Preview)
	b.WriteString("\n</persisted-output>")
	return b.String()
}

// Cleanup removes the entire tool-results directory for this session.
func (d *DiskResultPersister) Cleanup() error {
	return os.RemoveAll(filepath.Join(d.sessionDir, toolResultsDir))
}

// buildPreview creates a truncated preview of the result content.
// Takes the first 2000 bytes; cuts at the last newline boundary (if within 50% threshold).
func buildPreview(result string) string {
	bytes := []byte(result)
	if len(bytes) <= previewMaxBytes {
		return result
	}

	preview := bytes[:previewMaxBytes]
	cutIdx := strings.LastIndex(string(preview), "\n")
	if cutIdx >= previewMinNewline && cutIdx < previewMaxBytes-1 {
		return string(bytes[:cutIdx]) + "\n..."
	}

	return string(preview) + "\n..."
}
