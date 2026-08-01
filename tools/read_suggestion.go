package tools

// Suggestion* 常量是 ReadResult.Data["_suggestion"] 的结构化值。
// 使用类型安全的常量而非字符串字面量，避免 Executor 侧匹配错误。
const (
	SuggestionReadComplete     = "read_complete"     // 正常读取，无剩余
	SuggestionHasMoreLines     = "has_more_lines"    // 有剩余行未读
	SuggestionFileTooLarge     = "file_too_large"    // 文件过大（预检拒绝）
	SuggestionContentUnchanged = "content_unchanged" // 文件未变化
	SuggestionDocConverted     = "doc_converted"     // 文档转换完成
	SuggestionImageRead        = "image_read"        // 图片已读取
	SuggestionImageFailed      = "image_failed"      // 图片读取失败
	SuggestionEmptyFile        = "empty_file"        // 文件为空
	SuggestionPermissionDenied = "permission_denied" // 无读取权限
	SuggestionIsDirectory      = "is_directory"      // 路径是目录
	SuggestionFileNotFound     = "file_not_found"    // 文件不存在
)
