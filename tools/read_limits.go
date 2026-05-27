package tools

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
}

// DefaultFileReadingLimits returns the default file reading limits.
func DefaultFileReadingLimits() FileReadingLimits {
	return FileReadingLimits{
		MaxSizeBytes: 256 * 1024, // 256KB
		MaxTokens:    25000,
		DefaultLines: 500,
	}
}
