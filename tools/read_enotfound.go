package tools

import (
	"fmt"
	"path/filepath"
)

// ENOENTSuggestion 返回 CWD 路径补全建议。
// 简化设计（见过度设计审计 #7）：去掉了 Levenshtein 相似文件名搜索和目录列表，
// 只保留 CWD 前缀建议（零 I/O）。ENOENT 本身是模型路径拼写错误最常见的原因，
// 模型看到 ENOENT 后会自行 ls 目录或更正路径。
func ENOENTSuggestion(filePath, projectDir string) string {
	// 检查路径是否缺少 projectDir 前缀
	if !filepath.IsAbs(filePath) && projectDir != "" {
		// 不做 stat 检查（评估 I/O，且错误消息中提示路径拼写问题即可）
		return fmt.Sprintf(
			"路径 %q 在当前工作区内不存在。如果您确认文件存在，"+
				"请检查路径是否拼写正确。您可以使用 Glob 或 ls 工具查找文件。",
			filePath,
		)
	}
	return fmt.Sprintf(
		"文件 %q 不存在。请检查路径是否正确。使用 Glob 或 ls 工具查找目标文件。",
		filePath,
	)
}
