package tools

import (
	"fmt"
	"path/filepath"
)

// ENOENTSuggestion 返回 CWD 路径补全建议。
// 简化设计（见过度设计审计 #7）：去掉了 Levenshtein 相似文件名搜索和目录列表，
// 只保留 CWD 前缀建议（零 I/O）。ENOENT 本身是模型路径拼写错误最常见的原因，
// 模型看到 ENOENT 后会自行 ls 目录或更正路径。
// 采用第一人称引导格式（我做了什么 → 原因 → 下一步我应该怎么做）。
func ENOENTSuggestion(filePath, projectDir string) string {
	// 检查路径是否缺少 projectDir 前缀
	if !filepath.IsAbs(filePath) && projectDir != "" {
		return BuildGuide(
			fmt.Sprintf("尝试读取文件 %q，但该文件在当前工作区内不存在", filePath),
			"目标路径在文件系统中不存在（ENOENT），可能是相对路径基准不对或文件确实不存在",
			"检查路径是否拼写正确；使用 Glob 或 Ls 工具查找目标文件，确认正确路径后重新读取",
		)
	}
	return BuildGuide(
		fmt.Sprintf("尝试读取文件 %q，但该文件不存在", filePath),
		"目标路径在文件系统中不存在（ENOENT），可能是路径拼写或大小写有误",
		"使用 Glob 或 Ls 工具查找目标文件，确认正确路径后重新读取",
	)
}
