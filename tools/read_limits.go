package tools

import "sync"

// FileReadingLimits defines the hard limits for file reading tools.
// Inspired by ClueCode's cludecode/tools/FileReadTool/limits.ts
type FileReadingLimits struct {
	// MaxSizeBytes is the maximum file size in bytes that will be accepted.
	// Checked before reading (pre-read check). Default: 256KB.
	MaxSizeBytes int64 `json:"max_size_bytes" yaml:"max_size_bytes"`

	// MaxTokens is the maximum number of tokens allowed in the output.
	// Checked after reading (post-read check). Default: 25,000 tokens.
	MaxTokens int `json:"max_tokens" yaml:"max_tokens"`

	// DefaultLines is the maximum number of lines to read when no explicit limit is provided.
	DefaultLines int `json:"default_lines" yaml:"default_lines"`

	// DynamicDefaultLines enables dynamic default line count based on file size.
	// When enabled, DefaultLines is interpreted as a maximum, and the actual
	// default is calculated as min(DefaultLines, fileSize / 200 + 50).
	DynamicDefaultLines bool `json:"dynamic_default_lines" yaml:"dynamic_default_lines"`

	// EnableImageReading enables image file reading support.
	// When enabled, image files are read, compressed, and dispatched via ReadHook.
	// Default: false (images treated as unsupported binary).
	EnableImageReading bool `json:"enable_image_reading" yaml:"enable_image_reading"`
}

// DefaultFileReadingLimits returns the default file reading limits.
func DefaultFileReadingLimits() FileReadingLimits {
	return FileReadingLimits{
		MaxSizeBytes: 256 * 1024, // 256KB
		MaxTokens:    25000,
		DefaultLines: 500,
	}
}

// configLock 确保配置只被锁定一次（不可在会话中途变更）。
var configLock sync.Once

// dynamicDefaultLines 计算动态默认行数。
//
// 公式：min(DefaultLines, fileLines/20 + 50)
//   - 小文件（< 1000 行）：150 行默认 → 完整内容大概率一次读完
//   - 中等文件（~5000 行）：300 行默认 → 避免一次读完大文件
//   - 大文件（> 9000 行）：500 行默认 → 限制在 DefaultLines 上限
//
// 参数：
//   - totalLines: 文件总行数
//   - maxLines:   DefaultLines（默认行数上限）
//
// 返回：
//   - int: 计算后的默认行数
func dynamicDefaultLines(totalLines, maxLines int) int {
	candidate := totalLines/20 + 50
	if candidate > maxLines {
		return maxLines
	}
	return candidate
}

