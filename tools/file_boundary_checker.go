package tools

import (
	"fmt"
)

// FileBoundaryChecker 检查文件工具的路径是否在工作区内。
// 如果路径超出工作区边界，返回 PermissionAsk 以触发用户审批。
type FileBoundaryChecker struct {
	projectDir string
	sessionDir string
}

// NewFileBoundaryChecker 创建一个文件边界权限检查器。
// 参数：
//   - projectDir: 项目目录（用于解析相对路径并作为工作区边界锚点）
//   - sessionDir: 会话沙箱目录（用于解析 session: 前缀路径）
//
// 返回：
//   - ToolPermissionChecker: 文件边界检查器实例
func NewFileBoundaryChecker(projectDir, sessionDir string) ToolPermissionChecker {
	return &FileBoundaryChecker{
		projectDir: projectDir,
		sessionDir: sessionDir,
	}
}

// CheckPermissions 检查文件工具的目标路径是否在允许的工作区边界内。
// 只对 Write 和 FileEdit 工具生效；其他工具返回 Allow。
//
// 当路径超出工作区边界时，返回 PermissionAsk 而不是直接拒绝，
// 从而给用户审批的机会（executor 层会通过 awaitUserResponse 处理）。
//
// 安全检查：
//   - 路径解析和规范化（ResolveTargetPath）
//   - 工作区边界强制（ValidateFileSafety → enforceWorkspaceBoundary）
//   - 敏感文件保护（ValidateFileSafety → checkSensitiveFiles）
func (c *FileBoundaryChecker) CheckPermissions(ctx *ToolUseContext) PermissionResult {
	// 只处理文件操作工具
	if ctx.ToolName != "Write" && ctx.ToolName != "FileEdit" {
		return PermissionResult{Behavior: PermissionAllow}
	}

	// 提取路径参数（Write 用 "path"，FileEdit 用 "path"）
	path, _ := ctx.Params["path"].(string)
	if path == "" {
		return PermissionResult{Behavior: PermissionAllow}
	}

	// 解析路径，确定最终目标路径和作用域
	resolvedPath, _ := ResolveTargetPath(path, c.projectDir, c.sessionDir)
	if resolvedPath == "" {
		return PermissionResult{Behavior: PermissionAllow}
	}

	// 执行文件安全检查（工作区边界 + 敏感文件）
	if err := ValidateFileSafety(resolvedPath, c.projectDir); err != nil {
		return PermissionResult{
			Behavior: PermissionAsk,
			Message:  fmt.Sprintf("File %q resolves to %q which is outside the workspace.\n%s\n\nDo you want to allow this operation?", path, resolvedPath, err.Error()),
		}
	}

	return PermissionResult{Behavior: PermissionAllow}
}
