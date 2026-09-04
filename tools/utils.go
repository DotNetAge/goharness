package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/DotNetAge/goharness/sandbox"
)

// ValidateRequired 检查 params map 中是否存在某个必填参数。
// 若该 key 不存在，则返回一个带工具名的引导式错误。
//
// 示例：
//
//	if err := ValidateRequired("Read", params, "path"); err != nil {
//	    return nil, err
//	}
func ValidateRequired(toolName string, params map[string]any, key string) error {
	if _, ok := GetParam(params, key); !ok {
		return fmt.Errorf("%s", GuideMissingParam(toolName, key))
	}
	return nil
}

// ValidateRequiredString 校验某个必填参数是否存在且为 string 类型。
// 返回该字符串值以及校验失败时的错误。
//
// 此函数执行两项检查：
//  1. 确保该参数 key 存在
//  2. 验证值为 string 类型
//
// 返回：
//   - 校验通过时的字符串值
//   - 描述校验失败原因的错误
func ValidateRequiredString(toolName string, params map[string]any, key string) (string, error) {
	if err := ValidateRequired(toolName, params, key); err != nil {
		return "", err
	}

	raw, _ := GetParam(params, key)
	str, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s", GuideWrongParamType(toolName, key, "string", raw))
	}
	return str, nil
}

// ValidateFileSafety 通过路径锚定与 TOCTOU 防护来校验文件访问安全性。
// 它通过 filepath.Clean 规范化路径、解析符号链接，并确保真实路径
// 始终位于允许的工作区边界之内。
//
// 安全特性：
//   - 路径规范化以消除目录穿越尝试（../）
//   - 符号链接解析以防范基于符号链接的攻击
//   - 工作区边界强制约束以限制文件访问
//
// 职责边界（重要）：
// 此函数仅做路径解析与边界强制的机制性校验（供 SafeOpenFile /
// SafeCreateFile 在打开文件时做 TOCTOU 防护），不再承担任何安全
// 策略决策——敏感文件拦截等策略统一由沙箱（sandbox.Sandbox）负责。
//
// 参数：
//   - path: 待校验的文件路径（可为绝对路径或相对路径）
//   - projectDir: 用于锚定路径的项目目录。
//     若为空，则回退到 os.Getwd() 以保持向后兼容。
//
// 返回：
//   - 若路径可安全访问则返回 nil
//   - 描述访问被拒绝原因的错误
//
// 示例：
//
//	err := ValidateFileSafety("/project/file.txt", "/project")
//	if err != nil {
//	    // 处理被拒绝的访问
//	}
func ValidateFileSafety(path string, projectDir string) error {
	cleaned := filepath.Clean(path)

	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return fmt.Errorf("无法获取绝对路径: %w", err)
	}

	realPath, err := resolvePathSecurely(absPath)
	if err != nil {
		return fmt.Errorf("无法解析路径: %w", err)
	}

	realProjectDir, err := resolveProjectDir(projectDir)
	if err != nil {
		return fmt.Errorf("无法解析项目目录: %w", err)
	}

	return enforceWorkspaceBoundary(realPath, realProjectDir, path)
}

// resolvePathSecurely 对已存在的路径解析符号链接并执行安全检查。
// 对于不存在的路径，返回清理后的绝对路径以允许文件创建。
func resolvePathSecurely(absPath string) (string, error) {
	realPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return absPath, nil
		}
		return "", fmt.Errorf("无法解析符号链接: %w", err)
	}
	return realPath, nil
}

// resolveProjectDir 解析并校验项目目录路径。
func resolveProjectDir(projectDir string) (string, error) {
	if projectDir == "" {
		var err error
		projectDir, err = os.Getwd()
		if err != nil {
			return "", fmt.Errorf("无法获取工作目录: %w", err)
		}
	}

	realCwd, err := filepath.EvalSymlinks(projectDir)
	if err != nil {
		return projectDir, nil
	}
	return realCwd, nil
}

// enforceWorkspaceBoundary 确保解析后的路径位于项目目录之内。
func enforceWorkspaceBoundary(realPath, realProjectDir, originalPath string) error {
	if realPath == realProjectDir {
		return nil
	}

	// 根目录作为工作区时，所有绝对路径都在边界内
	if realProjectDir == "/" {
		return nil
	}

	sep := string(filepath.Separator)
	if !strings.HasPrefix(realPath, realProjectDir+sep) {
		return fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试访问路径 %q（解析为 %q），它位于工作区 %q 之外", originalPath, realPath, realProjectDir),
			"目标不在当前工作区边界内，属于越权操作；根目录等外部路径只有在获得用户同意后才能访问",
			"检查路径是否为工作区内的相对路径或绝对路径；若确实需要访问工作区之外的路径，先通过授权（PermissionAllow / PermissionAllowSession）获得用户同意后再执行",
		))
	}

	return nil
}

// EnforceSandboxFile 从 ctx 提取会话沙箱并对路径执行强制安全检查。
// 沙箱未注入时返回错误（工具自身不做授权检查，安全决策统一收口到沙箱）。
// 供增强版工具（如 mindx 的 ReadPro / LSPro）在自定义执行分支中复用，
// 与 Read / Write / Edit / Ls 的 Execute 保持一致。
func EnforceSandboxFile(ctx context.Context, resolvedPath string) error {
	sb, err := requireSandbox(ctx, "File")
	if err != nil {
		return err
	}
	return sb.EnforceFile(resolvedPath, GetToolContext(ctx).Session.ProjectDir())
}

// EnforceSandboxFileWithWhitelist 在 EnforceSandboxFile 基础上感知白名单豁免。
// extraAllowedDirs 为自带白名单的增强工具（如 mindx 的 LSPro）的
// 工具白名单（AddWhiteList）与会话白名单（PermissionAllowSession 记忆）的并集，
// 仅豁免目录边界检查；设备文件、敏感文件等硬性禁止不豁免。
// 语义与 Grant 阶段一致：Grant 白名单放行的路径，Execute 阶段同样放行。
func EnforceSandboxFileWithWhitelist(ctx context.Context, resolvedPath string, extraAllowedDirs []string) error {
	sb, err := requireSandbox(ctx, "File")
	if err != nil {
		return err
	}
	return sb.EnforceFileWithWhitelist(resolvedPath, GetToolContext(ctx).Session.ProjectDir(), extraAllowedDirs)
}

// sessionWhitelistDirs 提取指定工具的会话级白名单目录列表。
// 白名单条目来自用户此前的授权（PermissionAllowSession 记忆），
// 在 Execute 阶段透传给沙箱 EnforceFileWithWhitelist，
// 保证"授权后能真正执行"——仅豁免目录边界，硬性禁止不豁免。
// 工具未知或白名单为空时返回 nil。
func sessionWhitelistDirs(ctx context.Context, toolName string) []string {
	tc := GetToolContext(ctx)
	if tc == nil || tc.SessionWhitelist == nil {
		return nil
	}
	switch toolName {
	case "read":
		return tc.SessionWhitelist.Read
	case "write":
		return tc.SessionWhitelist.Write
	case "edit":
		return tc.SessionWhitelist.Edit
	case "ls":
		return tc.SessionWhitelist.Ls
	case "run_script":
		return tc.SessionWhitelist.RunScript
	default:
		return nil
	}
}

// requireSandbox 从 ctx 提取会话沙箱；未注入时返回引导式错误。
// 所有工具在执行安全相关操作前必须通过此函数获取沙箱，
// 未注入沙箱时一律拒绝执行（不再回退到工具内旧安全逻辑）。
func requireSandbox(ctx context.Context, toolName string) (*sandbox.Sandbox, error) {
	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		return nil, fmt.Errorf("%s", GuideSandboxRequired(toolName))
	}
	sb := tc.Session.Sandbox()
	if sb == nil {
		return nil, fmt.Errorf("%s", GuideSandboxRequired(toolName))
	}
	return sb, nil
}

// SafeOpenFile 在打开文件后校验路径以提供 TOCTOU 防护。
// 这可防止路径在校验与打开之间被篡改而导致的竞态条件。
//
// 参数：
//   - path: 待打开的文件路径（会被校验）
//   - projectDir: 项目目录边界
//   - flags: 打开标志（os.O_RDONLY、os.O_WRONLY 等）
//   - perm: 文件权限模式
//
// 返回：
//   - *os.File: 成功打开的文件句柄
//   - error: 校验或操作系统错误
//
// 安全性：
// 此函数在 Unix 系统上使用 O_NOFOLLOW 以防范符号链接攻击，
// 并在打开后重新校验解析后的路径以确保一致性。
func SafeOpenFile(path string, projectDir string, flags int, perm os.FileMode) (*os.File, error) {
	resolvedPath, err := resolveAndValidate(path, projectDir)
	if err != nil {
		return nil, err
	}

	safeFlags := applySecurityFlags(flags)

	file, err := os.OpenFile(resolvedPath, safeFlags, perm)
	if err != nil {
		return nil, fmt.Errorf("无法打开文件 %s: %w", resolvedPath, err)
	}

	if err := postOpenValidation(file, resolvedPath, projectDir); err != nil {
		file.Close()
		return nil, err
	}

	return file, nil
}

// SafeCreateFile 安全地创建一个新文件，提供 TOCTOU 防护。
// 它在创建前校验路径，并尽可能使用独占创建标志。
//
// 参数：
//   - path: 新文件的路径
//   - projectDir: 项目目录边界
//   - perm: 文件权限（默认：0644）
//
// 返回：
//   - *os.File: 创建后定位到开头的文件句柄
//   - error: 校验或创建错误
func SafeCreateFile(path string, projectDir string, perm os.FileMode) (*os.File, error) {
	if perm == 0 {
		perm = 0644
	}

	resolvedPath, err := resolveAndValidateForCreation(path, projectDir)
	if err != nil {
		return nil, err
	}

	file, err := os.OpenFile(resolvedPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, perm)
	if os.IsExist(err) {

		file, err = os.OpenFile(resolvedPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			return nil, fmt.Errorf("无法创建文件 %s: %w", resolvedPath, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("无法创建文件 %s: %w", resolvedPath, err)
	}

	if err := postOpenValidation(file, resolvedPath, projectDir); err != nil {
		file.Close()
		return nil, err
	}

	return file, nil
}

// resolveAndValidate 解析路径并执行完整的安全校验。
func resolveAndValidate(path string, projectDir string) (string, error) {
	if err := ValidateFileSafety(path, projectDir); err != nil {
		return "", err
	}

	absPath, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}

	return absPath, nil
}

// resolveAndValidateForCreation 解析用于文件创建的路径，对存在性检查有所放宽。
func resolveAndValidateForCreation(path string, projectDir string) (string, error) {
	cleaned := filepath.Clean(path)
	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("无法获取绝对路径: %w", err)
	}

	realProjectDir, err := resolveProjectDir(projectDir)
	if err != nil {
		return "", err
	}

	parentDir := filepath.Dir(absPath)
	realParent, err := filepath.EvalSymlinks(parentDir)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("无法解析父目录: %w", err)
	}

	if realParent != "" {
		if !strings.HasPrefix(realParent, realProjectDir+string(filepath.Separator)) &&
			realParent != realProjectDir {
			return "", fmt.Errorf("%s", BuildGuide(
				fmt.Sprintf("尝试访问位于工作区之外的路径（父目录 %q）", parentDir),
				fmt.Sprintf("目标路径的父目录 %q 位于工作区 %q 之外，属于越权操作", parentDir, realProjectDir),
				"先自查：我传入的路径是否意图访问工作区之外的资源？工作区之外的路径只有在获得用户同意后才能访问（可使用 PermissionAllow / PermissionAllowSession 授权，或用 PermissionDeny 拒绝）；若路径有误，应改用工作区内的路径",
			))
		}
	}

	return absPath, nil
}

// postOpenValidation 在文件打开后执行额外校验。
// 用于捕获路径在校验与打开之间被篡改的 TOCTOU 竞态。
func postOpenValidation(_ *os.File, resolvedPath string, projectDir string) error {
	actualPath, err := filepath.EvalSymlinks(resolvedPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("post-open validation failed: %w", err)
	}

	realProjectDir, _ := resolveProjectDir(projectDir)
	sep := string(filepath.Separator)

	if actualPath != resolvedPath &&
		!strings.HasPrefix(actualPath, realProjectDir+sep) &&
		actualPath != realProjectDir {
		return fmt.Errorf(
			"安全违规: 文件路径在打开后发生变化 (预期 %s, 实际 %s)",
			resolvedPath,
			actualPath,
		)
	}

	return nil
}

// TruncateString 将字符串截断为最多 maxLen 个 rune，若发生截断则追加 "..."。
// 它按 rune 计数以安全处理多字节字符（Unicode）。
//
// 参数：
//   - s: 待截断的输入字符串
//   - maxLen: 保留的最大 rune 数（不是字节数！）
//
// 返回：
//   - 若发生截断则返回追加了 "..." 的截断字符串
//   - 若 len(s) <= maxLen 则返回原字符串
//
// 边界情况：
//   - 若 maxLen <= 3，则精确返回 maxLen 个 rune，不追加 "..."
//   - 空字符串原样返回
//
// 示例：
//
//	truncated := TruncateString("Hello, 世界!", 8)
//	// 结果: "Hello, 世..."
func TruncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// pathWithinScope 判断 child 是否位于 parent 范围内（含边界分隔符）。
// 用于白名单路径匹配，避免前缀误匹配（例：parent=/tmp/foo 时，
// /tmp/foobar 不在范围内，/tmp/foo/bar 在范围内）；两者相等也视为在范围内。
func pathWithinScope(parent, child string) bool {
	if parent == child {
		return true
	}
	return strings.HasPrefix(child, parent+string(filepath.Separator))
}

// ToFloat64 将任意常见数值类型转换为 float64。
// 在处理 JSON 反序列化的数字时很有用，它们默认可能就是 float64。
//
// 支持的类型：
//   - float64、float32
//   - int、int32、int64
//
// 返回：
//   - 转换后的 float64 值
//   - 转换成功返回 true，类型不支持返回 false
//
// 示例：
//
//	val, ok := ToFloat64(42)
//	// val = 42.0, ok = true
//
//	val, ok := ToFloat64("hello")
//	// val = 0, ok = false
func ToFloat64(v any) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case int32:
		return float64(val), true
	default:
		return 0, false
	}
}

// PathScope 表示文件路径解析后的作用域。
// 它指示某个路径解析为项目目录还是会话沙箱。
type PathScope string

const (
	// PathScopeProject 表示路径解析为项目目录。
	PathScopeProject PathScope = "project"

	// PathScopeSession 表示路径解析为会话沙箱目录。
	PathScopeSession PathScope = "session"
)

const sessionPathPrefix = "session:"

// ResolveTargetPath 解析文件路径，支持可选的 session: 前缀。
// 这为显式目录选择提供了最小的语法糖，不做启发式推断。
//
// 行为：
//   - "session:filename" → <sessionDir>/filename（作用域：session）
//     若 sessionDir 为空则回退到 <projectDir>/filename。
//   - "relative/path" → <projectDir>/relative/path（作用域：project）
//     若 projectDir 为空则回退到 CWD。
//   - "/absolute/path" → 原样返回（作用域：空）
//
// 设计哲学：
// 目录语义应通过 System Prompt 引导（Agent Native 方式），
// 而非从上下文中推断。这使行为可预测且可调试。
//
// 参数：
//   - inputPath: 待解析的路径（可包含 session: 前缀）
//   - projectDir: 相对路径所依据的项目目录
//   - sessionDir: session: 前缀路径所依据的会话沙箱目录
//
// 返回：
//   - absPath: 解析后的绝对路径
//   - scope: 指示路径解析到何处的作用域
//
// 示例：
//
//	path, scope := ResolveTargetPath("file.txt", "/project", "/sessions/abc")
//	// path = "/sessions/abc/file.txt", scope = PathScopeSession
//
//	path, scope := ResolveTargetPath("/etc/passwd", "/project", "")
//	// path = "/etc/passwd", scope = ""（绝对路径时为空）
func ResolveTargetPath(inputPath string, projectDir, sessionDir string) (absPath string, scope PathScope) {
	if inputPath == "" {
		return "", PathScopeProject
	}

	// 展开 ~ / ~/ 前缀（shell 语义），使工具可接受 home 目录相对路径。
	// 修复前 "~/workspaces" 会被 filepath.Join(projectDir, "~/workspaces")
	// 错误拼接为 "<projectDir>/~/workspaces"。
	if inputPath == "~" || strings.HasPrefix(inputPath, "~/") {
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			if inputPath == "~" {
				inputPath = home
			} else {
				inputPath = filepath.Join(home, inputPath[2:])
			}
		}
	}

	if filepath.IsAbs(inputPath) {
		return inputPath, ""
	}

	if strings.HasPrefix(inputPath, sessionPathPrefix) {
		filename := strings.TrimPrefix(inputPath, sessionPathPrefix)
		targetDir := sessionDir

		if targetDir == "" {
			targetDir = projectDir
			scope = PathScopeProject
		} else {
			scope = PathScopeSession
		}

		return filepath.Join(targetDir, filename), scope
	}

	targetDir := projectDir
	if targetDir == "" {
		targetDir, _ = os.Getwd()
	}

	return filepath.Join(targetDir, inputPath), PathScopeProject
}

// SessionContextKey 是用于存储会话 ID 值的 context key。
// 它被定义为一个未导出类型以防止 context key 冲突。
type SessionContextKey struct{}

// ExtractSessionID 从 context.Context 中提取会话 ID。
// 若未找到会话 ID 或其不是字符串，则返回空字符串。
//
// 用法：
//
//	sessionID := ExtractSessionID(ctx)
//	if sessionID != "" {
//	    log.Printf("Processing request for session %s", sessionID)
//	}
func ExtractSessionID(ctx context.Context) string {
	if sessionID, ok := ctx.Value(SessionContextKey{}).(string); ok {
		return sessionID
	}
	return ""
}

// WithSessionID 返回一个携带给定会话 ID 的新 context.Context。
// 之后可通过 ExtractSessionID 取回该会话 ID。
//
// 通常用于在调用链中传递会话信息，
// 以便进行日志记录、链路追踪和权限校验。
//
// 示例：
//
//	ctx := WithSessionID(context.Background(), "session-123")
//	sessionID := ExtractSessionID(ctx)
//	// sessionID = "session-123"
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, SessionContextKey{}, sessionID)
}
