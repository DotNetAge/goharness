package tools

// ReadData 是 Read 工具输出的强类型数据结构。
//
// 见过度设计审计 M2：从 map[string]any 改为强类型 struct，消除类型逃生口。
// 保持向后兼容：ReadResult.Data 仍然是一个 map[string]any 兼容字段，
// 但所有结构化字段都通过 ReadData 直接访问。
type ReadData struct {
	Success bool   `json:"success"`
	Path    string `json:"path"`
	Scope   string `json:"scope,omitempty"`
	Content string `json:"content,omitempty"`

	// 元数据
	SizeBytes  int64 `json:"size_bytes,omitempty"`
	LinesRead  int   `json:"lines_read,omitempty"`
	TotalLines int   `json:"total_lines,omitempty"`
	StartLine  int   `json:"start_line,omitempty"`

	// 分页信息
	HasMore    bool `json:"has_more,omitempty"`
	NextOffset int  `json:"next_offset,omitempty"`

	// 行为指令（Actionable，前缀 _ 标记为非标准化字段）
	Suggestion string `json:"_suggestion"`
	Note       string `json:"_note,omitempty"`

	// 文档转换元数据
	Format string `json:"format,omitempty"`
	Title  string `json:"title,omitempty"`
	Author string `json:"author,omitempty"`
	Pages  int    `json:"pages,omitempty"`
}

// AsMap 将 ReadData 转换为 map[string]any，保持向后兼容。
func (d *ReadData) AsMap() map[string]any {
	m := map[string]any{
		"success":     d.Success,
		"path":        d.Path,
		"_suggestion": d.Suggestion,
	}
	if d.Scope != "" {
		m["scope"] = d.Scope
	}
	if d.Content != "" {
		m["content"] = d.Content
	}
	if d.SizeBytes > 0 {
		m["size_bytes"] = d.SizeBytes
	}
	if d.LinesRead > 0 {
		m["lines_read"] = d.LinesRead
	}
	if d.TotalLines > 0 {
		m["total_lines"] = d.TotalLines
	}
	if d.StartLine > 0 {
		m["start_line"] = d.StartLine
	}
	if d.HasMore {
		m["has_more"] = true
		m["next_offset"] = d.NextOffset
	}
	if d.Note != "" {
		m["_note"] = d.Note
	}
	if d.Format != "" {
		m["format"] = d.Format
	}
	if d.Title != "" {
		m["title"] = d.Title
	}
	if d.Author != "" {
		m["author"] = d.Author
	}
	if d.Pages > 0 {
		m["pages"] = d.Pages
	}
	return m
}

// ReadResult 是 Read.Execute 的正式返回值类型。
//
// 层级设计：
//
//	Data       — 强类型 ReadData 输出（序列化为 JSON）
//	Images     — 图片数据层（不直接序列化，由 Executor 处理）
type ReadResult struct {
	Data   *ReadData      `json:"data"`
	Images []ImageContent `json:"-"`
}

// ImageContent 是压缩并编码后的图片数据。
type ImageContent struct {
	MediaType      string `json:"media_type"`
	Base64Data     string `json:"base64_data"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	RawSize        int64  `json:"raw_size"`
	CompressedSize int    `json:"compressed_size"`
}

// EditResult 是 Edit 工具的结构化输出。
// 见 EDIT_DESIGN.md D 节。
type EditResult struct {
	Success  bool   `json:"success"`
	FilePath string `json:"file_path"`
	Scope    string `json:"scope,omitempty"`

	// 替换统计
	ReplaceCount int    `json:"replace_count"`
	TotalMatches int    `json:"total_matches,omitempty"`
	ReplaceMode  string `json:"replace_mode"` // "single" | "all" | "limit:N" | "create"

	// 变更差异（仅 P2 场景启用）
	Diff string `json:"diff,omitempty"`
}

// WriteResult 是 Write 工具的结构化输出。
// 见 WRITE_DESIGN.md B 节。
type WriteResult struct {
	Success  bool   `json:"success"`
	Type     string `json:"type"` // "create" | "overwrite" | "append"
	FilePath string `json:"file_path"`
	Scope    string `json:"scope,omitempty"`

	// 写入统计
	BytesWritten int   `json:"bytes_written"`
	TotalSize    int64 `json:"total_size,omitempty"`

	// 变更差异（仅 overwrite + P2 场景启用）
	Diff string `json:"diff,omitempty"`
}

// Message 是 SideEffect 消息载体。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
