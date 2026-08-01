package tools

// FileReadingLimits 定义文件读取工具的硬性限制。
type FileReadingLimits struct {
	// MaxSizeBytes 是允许读取的最大文件字节数。
	// 在读取前检查（预读检查）。默认：256KB。
	MaxSizeBytes int64 `json:"max_size_bytes" yaml:"max_size_bytes"`

	// MaxOutputChars 是读取结果允许的最大输出字符数。
	// 在读取后检查（后读检查）。默认：75000 字符。
	// 文本与文档转换（PDF/DOCX/XLSX/EPUB）统一按此预算检查，
	// 超过预算时返回错误并引导用 offset/limit 分页精读。
	MaxOutputChars int `json:"max_output_chars" yaml:"max_output_chars"`

	// DefaultLines 是未显式指定 limit 时的默认最大读取行数。
	DefaultLines int `json:"default_lines" yaml:"default_lines"`

	// DynamicDefaultLines 启用后，DefaultLines 视为上限，
	// 实际默认行数按 min(DefaultLines, totalLines/20 + 50) 动态计算。
	DynamicDefaultLines bool `json:"dynamic_default_lines" yaml:"dynamic_default_lines"`

	// EnableImageReading 启用图片文件读取。
	// 启用后图片被读取、压缩并内联返回（不再走 ReadHook）。
	// 默认：false（图片视为不支持处理的二进制文件）。
	EnableImageReading bool `json:"enable_image_reading" yaml:"enable_image_reading"`
}

// DefaultFileReadingLimits 返回默认的文件读取限制。
func DefaultFileReadingLimits() FileReadingLimits {
	return FileReadingLimits{
		MaxSizeBytes:   256 * 1024, // 256KB
		MaxOutputChars: 75000,
		DefaultLines:   500,
	}
}

// dynamicDefaultLines 计算动态默认行数。
//
// 公式：min(DefaultLines, totalLines/20 + 50)
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
