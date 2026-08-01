package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateRequired checks that a required parameter exists in the params map.
// It returns a guided error (带工具名）if the key is not present.
//
// Example:
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

// ValidateRequiredString validates that a required parameter exists and is of string type.
// It returns the string value and an error if validation fails.
//
// This function performs two checks:
//  1. Ensures the parameter key exists
//  2. Verifies the value is a string type
//
// Returns:
//   - The string value if valid
//   - An error describing what validation failed
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

// ValidateFileSafety verifies file access safety using path anchoring with TOCTOU protection.
// It normalizes the path via filepath.Clean, resolves symlinks, and ensures
// the real path stays within the allowed workspace boundary.
//
// Security Features:
//   - Path normalization to eliminate directory traversal attempts (../)
//   - Symlink resolution to prevent symlink-based attacks
//   - Workspace boundary enforcement to restrict file access
//   - Sensitive system file protection (.env, SSH keys, etc.)
//
// Parameters:
//   - path: The file path to validate (absolute or relative)
//   - projectDir: The project directory to anchor paths against.
//     If empty, falls back to os.Getwd() for backward compatibility.
//
// Returns:
//   - nil if the path is safe to access
//   - An error describing why access was denied
//
// Example:
//
//	err := ValidateFileSafety("/project/file.txt", "/project")
//	if err != nil {
//	    // Handle denied access
//	}
//
// Security Considerations:
// This function provides design-time safety validation. For runtime protection,
// use SafeOpenFile or SafeCreateFile which perform atomic open-and-validate operations.
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

	if err := enforceWorkspaceBoundary(realPath, realProjectDir, path); err != nil {
		return err
	}

	if err := checkSensitiveFiles(realPath); err != nil {
		return err
	}

	return nil
}

// resolvePathSecurely resolves symlinks for existing paths with security checks.
// For non-existent paths, it returns the cleaned absolute path to allow file creation.
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

// resolveProjectDir resolves and validates the project directory path.
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

// enforceWorkspaceBoundary ensures the resolved path stays within the project directory.
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

// checkSensitiveFiles blocks access to sensitive system files.
func checkSensitiveFiles(realPath string) error {
	baseName := filepath.Base(realPath)

	sensitiveFiles := map[string]bool{
		".env":        true,
		"id_rsa":      true,
		"id_ed25519":  true,
		"passwd":      true,
		"shadow":      true,
		"sudoers":     true,
		".ssh_config": true,
		"known_hosts": true,
	}

	if sensitiveFiles[baseName] {
		return fmt.Errorf("%s", GuideSensitiveFile(realPath))
	}

	return nil
}

// CheckSensitiveFiles 检查路径是否为敏感文件（.env / .ssh 密钥等）。
// 返回错误表示命中敏感文件列表，该检查是硬性安全边界，授权不可覆盖。
// 供同工具族的增强版工具（如 mindx 的 ReadPro / LSPro）在自定义执行分支中复用，
// 与 Edit / Read 的 Execute 保持一致。
func CheckSensitiveFiles(path string) error {
	return checkSensitiveFiles(path)
}

// SafeOpenFile opens a file with TOCTOU protection by validating the path after opening.
// This prevents race conditions where the path could be changed between validation and opening.
//
// Parameters:
//   - path: File path to open (will be validated)
//   - projectDir: Project directory boundary
//   - flags: Open flags (os.O_RDONLY, os.O_WRONLY, etc.)
//   - perm: File permissions mode
//
// Returns:
//   - *os.File: Successfully opened file handle
//   - error: Validation or OS error
//
// Security:
// This function uses O_NOFOLLOW on Unix systems to prevent symlink attacks,
// and re-validates the resolved path after opening to ensure consistency.
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

// SafeCreateFile safely creates a new file with TOCTOU protection.
// It validates the path before creation and uses exclusive create flags when possible.
//
// Parameters:
//   - path: Path for the new file
//   - projectDir: Project directory boundary
//   - perm: File permissions (default: 0644)
//
// Returns:
//   - *os.File: Created file handle positioned at the beginning
//   - error: Validation or creation error
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

// resolveAndValidate resolves a path and performs full safety validation.
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

// resolveAndValidateForCreation resolves a path for file creation with relaxed existence checks.
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

// postOpenValidation performs additional validation after a file has been opened.
// This catches TOCTOU races where the path changed between validation and opening.
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

// TruncateString truncates a string to maxLen runes, appending "..." if truncated.
// It counts by runes to safely handle multi-byte characters (Unicode).
//
// Parameters:
//   - s: The input string to truncate
//   - maxLen: Maximum number of runes to keep (not bytes!)
//
// Returns:
//   - Truncated string with "..." appended if truncation occurred
//   - Original string if len(s) <= maxLen
//
// Edge Cases:
//   - If maxLen <= 3, returns exactly maxLen runes without appending "..."
//   - Empty strings are returned as-is
//
// Example:
//
//	truncated := TruncateString("Hello, 世界!", 8)
//	// Result: "Hello, 世..."
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

// ToFloat64 converts a numeric value of any common numeric type to float64.
// This is useful when dealing with JSON unmarshaled numbers which may be float64 by default.
//
// Supported Types:
//   - float64, float32
//   - int, int32, int64
//
// Returns:
//   - The converted float64 value
//   - true if conversion succeeded, false if type is unsupported
//
// Example:
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

// PathScope represents the resolved scope of a file path.
// It indicates whether a path resolves to the project directory or session sandbox.
type PathScope string

const (
	// PathScopeProject indicates the path resolves to the project directory.
	PathScopeProject PathScope = "project"

	// PathScopeSession indicates the path resolves to the session sandbox directory.
	PathScopeSession PathScope = "session"
)

const sessionPathPrefix = "session:"

// ResolveTargetPath resolves a file path with optional session: prefix.
// This provides minimal syntax sugar for explicit directory selection without heuristic inference.
//
// Behavior:
//   - "session:filename" → <sessionDir>/filename (scope: session)
//     Falls back to <projectDir>/filename if sessionDir is empty.
//   - "relative/path" → <projectDir>/relative/path (scope: project)
//     Falls back to CWD if projectDir is empty.
//   - "/absolute/path" → returned as-is (scope: empty)
//
// Design Philosophy:
// Directory semantics should be guided via System Prompt (Agent Native approach),
// not inferred from context. This keeps behavior predictable and debuggable.
//
// Parameters:
//   - inputPath: The path to resolve (may include session: prefix)
//   - projectDir: Project directory for relative paths
//   - sessionDir: Session sandbox directory for session: prefixed paths
//
// Returns:
//   - absPath: Resolved absolute path
//   - scope: PathScope indicating where the path resolves
//
// Example:
//
//	path, scope := ResolveTargetPath("file.txt", "/project", "/sessions/abc")
//	// path = "/sessions/abc/file.txt", scope = PathScopeSession
//
//	path, scope := ResolveTargetPath("/etc/passwd", "/project", "")
//	// path = "/etc/passwd", scope = "" (empty for absolute paths)
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

// SessionContextKey is the context key for storing session ID values.
// It's defined as an unexported type to prevent context key collisions.
type SessionContextKey struct{}

// ExtractSessionID extracts the session ID from a context.Context.
// Returns empty string if no session ID is found or if it's not a string.
//
// Usage:
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

// WithSessionID returns a new context.Context that carries the given session ID.
// The session ID can later be retrieved using ExtractSessionID.
//
// This is typically used to propagate session information through the call chain
// for logging, tracing, and permission checking purposes.
//
// Example:
//
//	ctx := WithSessionID(context.Background(), "session-123")
//	sessionID := ExtractSessionID(ctx)
//	// sessionID = "session-123"
func WithSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, SessionContextKey{}, sessionID)
}
