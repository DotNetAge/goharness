package core

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
		MaxSizeBytes:  256 * 1024, // 256KB
		MaxTokens:     25000,
		DefaultLines:  500,
	}
}

// ToolResultLimits defines limits for tool execution results to prevent context explosion.
// Inspired by ClueCode's cludecode/utils/toolResultStorage.ts
type ToolResultLimits struct {
	// MaxResultSizeChars is the per-tool result size threshold in characters.
	// Results exceeding this will be persisted to disk.
	// Default: 50,000 characters.
	MaxResultSizeChars int `json:"max_result_size_chars" yaml:"max_result_size_chars"`

	// MaxToolResultsPerMessageChars is the total size of all tool results
	// within a single message cycle, in characters.
	// Default: 200,000 characters.
	MaxToolResultsPerMessageChars int `json:"max_tool_results_per_message_chars" yaml:"max_tool_results_per_message_chars"`
}

// DefaultToolResultLimits returns the default tool result limits.
func DefaultToolResultLimits() ToolResultLimits {
	return ToolResultLimits{
		MaxResultSizeChars:           50000,
		MaxToolResultsPerMessageChars: 200000,
	}
}

// PersistedToolResult represents a tool result that was too large for
// inline context and was persisted to disk. Only a preview and file path
// are kept in the context window.
type PersistedToolResult struct {
	// ToolName is the name of the tool that produced this result.
	ToolName string `json:"tool_name" yaml:"tool_name"`

	// FullSize is the total character count of the original result.
	FullSize int `json:"full_size" yaml:"full_size"`

	// Preview is a truncated preview of the result (first N characters).
	Preview string `json:"preview" yaml:"preview"`

	// FilePath is the path to the persisted file on disk.
	FilePath string `json:"file_path" yaml:"file_path"`
}
