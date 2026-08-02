package sandbox

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// CheckFile 在 Grant 阶段调用，决定文件操作是否需要询问用户。
//
// 决策流程：
//  1. 路径不存在（含 ENOENT / ENOTDIR / EACCES）→ Allow（让 Execute 走兜底报错）
//  2. 命中设备文件路径 → Deny（功能保护，防进程挂起）
//  3. 命中敏感文件 glob / 精确路径 / 敏感目录段 → Deny（硬性边界，不可覆盖）
//  4. 在工作区内 → Allow
//  5. 在工作区外 → AskUser（触发权限弹窗）
//
// 注意：Grant 阶段不做 EvalSymlinks（避免对不存在的路径失败），
// 符号链接的真实路径解析在 EnforceFile 阶段做（防 TOCTOU）。
func (s *Sandbox) CheckFile(path string, projectDir string) FileDecision {
	p := s.policy.Load()

	// 1. 文件不存在或无法访问 → Allow（让 Execute 报错）
	//    覆盖 ENOENT / ENOTDIR / EACCES，避免对不存在的路径弹授权窗。
	if _, statErr := os.Stat(path); statErr != nil {
		return FileDecision{Decision: DecisionAllow}
	}

	// 2. 硬性禁止：设备文件路径
	if s.isDevicePath(path, p) {
		return FileDecision{
			Decision: DecisionDeny,
			Reason:   GuideDeviceFile(path),
		}
	}

	// 3. 硬性禁止：敏感文件 glob 匹配
	if s.isDeniedFile(path, p) {
		return FileDecision{
			Decision: DecisionDeny,
			Reason:   GuideSensitiveFile(path),
		}
	}

	// 4. 硬性禁止：敏感目录段匹配
	if s.isInDeniedDir(path, p) {
		return FileDecision{
			Decision: DecisionDeny,
			Reason:   GuideSensitiveFile(path),
		}
	}

	// 5. 硬性禁止：精确路径匹配
	for _, denied := range p.DeniedFilePaths {
		if path == denied {
			return FileDecision{
				Decision: DecisionDeny,
				Reason:   GuideSensitiveFile(path),
			}
		}
	}

	// 6. 目录边界检查
	if s.isOutsideWorkspace(path, projectDir, p) {
		return FileDecision{
			Decision: DecisionAskUser,
			Reason:   GuideOutsideWorkspace(path, projectDir),
		}
	}

	return FileDecision{Decision: DecisionAllow}
}

// CheckFileAllowOrDeny 是 Glob 等不实现 PermissionRequired 接口的工具的简化决策路径。
//
// 与 CheckFile 的差异：
//   - 不返回 AskUser（这类工具不触发授权弹窗，直接拒绝越界访问）
//   - 用于 Execute 阶段直接判断"允许或拒绝"
//
// 决策流程：
//  1. 命中设备文件 / 敏感文件 / 敏感目录 / 精确路径 → Deny
//  2. 在工作区内 → Allow
//  3. 在工作区外 → Deny（不弹窗，直接拒绝）
//
// 注意：不做 EvalSymlinks（防 TOCTOU 应在真正读写时调用 EnforceFile）。
func (s *Sandbox) CheckFileAllowOrDeny(path string, projectDir string) FileDecision {
	dec := s.CheckFile(path, projectDir)
	if dec.Decision == DecisionAskUser {
		// 简化决策路径：越界直接拒绝，不弹窗
		return FileDecision{
			Decision: DecisionDeny,
			Reason:   GuideOutsideWorkspace(path, projectDir),
		}
	}
	return dec
}

// EnforceFile 在 Execute 阶段调用，做最终强制检查。
//
// 与 CheckFile 的差异：
//   - 不返回 AskUser（Execute 阶段不弹窗）
//   - 重新解析符号链接（防 TOCTOU：Grant 后文件可能被替换为符号链接）
//   - 失败时静默返回 error，由调用方决定如何处理
//
// 调用时机：工具的 Execute 在真正读写前调用。
func (s *Sandbox) EnforceFile(path string, projectDir string) error {
	p := s.policy.Load()

	// 解析符号链接，获取真实路径
	// 不存在的文件 EvalSymlinks 会失败，此时用原始路径做后续检查。
	realPath, err := filepath.EvalSymlinks(path)
	pathResolved := err == nil // path 是否成功解析符号链接（决定 projectDir 是否也需归一化）
	if err != nil {
		if !os.IsNotExist(err) {
			// 非 ENOENT 错误（如 EACCES）→ 视为可疑，拒绝
			return err
		}
		realPath = path
	}

	// 硬性禁止：设备文件路径（基于真实路径，防符号链接绕过）
	if s.isDevicePath(realPath, p) {
		return &DenyError{Reason: GuideDeviceFile(realPath)}
	}

	// 硬性禁止：敏感文件（基于真实路径，防符号链接绕过）
	if s.isDeniedFile(realPath, p) {
		return s.denyFileError(realPath)
	}

	// 硬性禁止：敏感目录段
	if s.isInDeniedDir(realPath, p) {
		return s.denyFileError(realPath)
	}

	// 硬性禁止：精确路径
	for _, denied := range p.DeniedFilePaths {
		if realPath == denied {
			return s.denyFileError(realPath)
		}
	}

	// 目录边界检查（基于真实路径）
	// 仅当 path 成功解析符号链接时，才对 projectDir 与 AllowedDirs 做 EvalSymlinks 归一化，
	// 保持与 realPath 解析方式一致。path 不存在时 realPath 保留原始路径，projectDir 也用原始路径，
	// 避免 macOS 上 /var → /private/var 符号链接导致两者前缀不匹配的误判。
	realProjectDir := projectDir
	realPolicy := p
	if pathResolved {
		realProjectDir = resolveSymlinks(projectDir)
		if len(p.AllowedDirs) > 0 {
			realDirs := make([]string, len(p.AllowedDirs))
			changed := false
			for i, dir := range p.AllowedDirs {
				realDirs[i] = resolveSymlinks(dir)
				if realDirs[i] != dir {
					changed = true
				}
			}
			if changed {
				tmp := *p
				tmp.AllowedDirs = realDirs
				realPolicy = &tmp
			}
		}
	}
	if s.isOutsideWorkspace(realPath, realProjectDir, realPolicy) {
		return s.outsideError(realPath, realProjectDir)
	}

	return nil
}

// resolveSymlinks 解析路径的符号链接真实路径。
// 解析失败（路径不存在等）时返回原始路径，由调用方兜底处理。
func resolveSymlinks(path string) string {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return real
}

// isDeniedFile 检查文件名是否命中敏感文件 glob 模式。
// 匹配 basename，大小写不敏感。
func (s *Sandbox) isDeniedFile(path string, p *SandboxPolicy) bool {
	if len(p.DeniedFileGlobs) == 0 {
		return false
	}
	base := strings.ToLower(filepath.Base(path))
	for _, glob := range p.DeniedFileGlobs {
		// 使用简化的 glob 匹配：支持 * 通配符
		if matchGlob(glob, base) {
			return true
		}
	}
	return false
}

// isInDeniedDir 检查路径中是否包含敏感目录段。
// 例：/Users/ray/.ssh/config 命中 .ssh 段。
// 大小写不敏感。
func (s *Sandbox) isInDeniedDir(path string, p *SandboxPolicy) bool {
	if len(p.DeniedDirGlobs) == 0 {
		return false
	}
	// 遍历路径每一段，检查是否命中
	cleanPath := filepath.Clean(path)
	parts := strings.Split(cleanPath, string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			continue
		}
		lowerPart := strings.ToLower(part)
		for _, glob := range p.DeniedDirGlobs {
			if matchGlob(glob, lowerPart) {
				return true
			}
		}
	}
	return false
}

// isDevicePath 检查路径是否命中设备文件黑名单。
// 精确匹配，大小写敏感（设备文件路径在 Unix 上是大小写敏感的）。
func (s *Sandbox) isDevicePath(path string, p *SandboxPolicy) bool {
	if len(p.DeniedDevicePaths) == 0 {
		return false
	}
	cleanPath := filepath.Clean(path)
	for _, device := range p.DeniedDevicePaths {
		if cleanPath == device {
			return true
		}
	}
	return false
}

// isOutsideWorkspace 检查路径是否在所有允许的目录之外。
//
// projectDir 始终被视为允许目录（会话级项目目录，运行时传入）。
// AllowedDirs 是额外的允许目录（如用户主目录 ~/.mindx），在策略编译时固定。
// 两者都为空时表示无目录限制（向后兼容，沙箱未配置 AllowedDirs 且无 projectDir 时放行）。
func (s *Sandbox) isOutsideWorkspace(path string, projectDir string, p *SandboxPolicy) bool {
	// projectDir 始终是允许的目录
	if projectDir != "" && pathWithinDir(path, projectDir) {
		return false
	}
	// AllowedDirs 是额外的允许目录
	for _, dir := range p.AllowedDirs {
		if pathWithinDir(path, dir) {
			return false
		}
	}
	// 两者都为空时无限制（向后兼容）
	if projectDir == "" && len(p.AllowedDirs) == 0 {
		return false
	}
	return true
}

// globRegexCache 缓存 glob 模式编译后的正则，避免重复编译。
// CheckFile/EnforceFile 在每次文件检查时都会调用 matchGlob，
// 缓存能显著降低高频调用场景的 CPU 开销。
var globRegexCache = make(map[string]*regexp.Regexp)

// matchGlob 实现简化的 glob 匹配，仅支持 * 通配符。
// 不引入 path/filepath.Match 是因为 filepath.Match 对 ? 和 [] 也有特殊语义，
// 而本场景只需要 * 即可覆盖 .env* / *.pem / credentials* 等常见模式。
//
// 实现方式：把 * 转为正则 .* 并锚定首尾，编译结果缓存复用。
func matchGlob(pattern, name string) bool {
	re, ok := globRegexCache[pattern]
	if !ok {
		// 转义非 * 的正则元字符，然后把 * 替换为 .*
		var buf strings.Builder
		buf.WriteString("^")
		for _, r := range pattern {
			if r == '*' {
				buf.WriteString(".*")
			} else {
				// 转义正则元字符
				if strings.ContainsRune(`\.+?()[]{}|^$`, r) {
					buf.WriteByte('\\')
				}
				buf.WriteRune(r)
			}
		}
		buf.WriteString("$")
		compiled, err := regexp.Compile(buf.String())
		if err != nil {
			return false
		}
		globRegexCache[pattern] = compiled
		re = compiled
	}
	return re.MatchString(name)
}

// pathWithinDir 检查 path 是否在 dir 子树内。
// 使用 filepath.Clean 归一化后做前缀比较，避免 .. 绕过。
func pathWithinDir(path, dir string) bool {
	cleanPath := filepath.Clean(path)
	cleanDir := filepath.Clean(dir)
	if cleanPath == cleanDir {
		return true
	}
	// 确保 path 是 dir 的子路径：dir 必须是 path 的前缀，且 path 紧跟分隔符
	return strings.HasPrefix(cleanPath, cleanDir+string(filepath.Separator))
}

// denyFileError 返回敏感文件拒绝错误。
// 不返回结构化 error，由调用方决定如何包装。
func (s *Sandbox) denyFileError(path string) error {
	return &DenyError{
		Reason: GuideSensitiveFile(path),
	}
}

// outsideError 返回越界拒绝错误。
func (s *Sandbox) outsideError(path, projectDir string) error {
	return &DenyError{
		Reason: GuideOutsideWorkspace(path, projectDir),
	}
}

// DenyError 是沙箱拒绝错误，承载可读的引导信息。
// 调用方可用 errors.As 提取 Reason 字段用于错误展示。
type DenyError struct {
	Reason string
}

// Error 实现 error 接口。
func (e *DenyError) Error() string {
	return e.Reason
}
