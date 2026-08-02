package sandbox

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strings"
)

// SandboxPolicy 是沙箱的不可变策略配置。
//
// 设计为值对象：一经 Compile 归一化后不可修改。
// 策略更新通过 Sandbox.UpdatePolicy 原子替换整个对象。
type SandboxPolicy struct {
	// ===== 文件访问策略 =====

	// AllowedDirs 是允许访问的目录树（绝对路径）。
	// 路径必须通过 EvalSymlinks 解析为真实路径后比较。
	// 空切片表示"无目录限制"（向后兼容旧行为，不推荐生产使用）。
	AllowedDirs []string

	// DeniedFileGlobs 是禁止访问的文件名 glob 模式（仅匹配 basename）。
	// 例："*.pem"、"*.key"、".env*"、"credentials*"
	// 大小写不敏感（跨平台兼容）。
	DeniedFileGlobs []string

	// DeniedFilePaths 是绝对禁止访问的文件路径（绝对路径，精确匹配）。
	// 这是硬性边界，授权不可覆盖。
	DeniedFilePaths []string

	// DeniedDirGlobs 是禁止访问的目录名 glob 模式（匹配路径中任意一段）。
	// 例：".ssh"、".aws"、".docker"、"secrets"
	// 路径中任意一段命中即拒绝，大小写不敏感。
	DeniedDirGlobs []string

	// DeniedDevicePaths 是禁止访问的设备文件路径（绝对路径，精确匹配）。
	// 读取这些文件会导致进程挂起（如 /dev/zero、/dev/random）。
	// 与 DeniedFilePaths 区分：这是"功能保护"而非"敏感数据保护"，
	// 但同样硬性拒绝，授权不可覆盖。
	DeniedDevicePaths []string

	// MaxFileBytes 是单次读取的最大字节数，0 表示不限。
	MaxFileBytes int64

	// ===== 网络访问策略 =====

	// NetworkDenySubnets 是禁止访问的 IP 段（CIDR）。
	// 默认包含所有私有 IP 段与云元数据段。
	NetworkDenySubnets []*net.IPNet

	// NetworkAllowSubnets 是显式允许的 IP 段（优先于 deny）。
	// 例：允许访问内网 CI 服务器 10.0.1.100/32
	NetworkAllowSubnets []*net.IPNet

	// ===== 命令执行策略 =====

	// AllowedCommands 是允许执行的命令白名单（basename，小写）。
	// 空切片表示"不启用命令白名单"（向后兼容，不推荐生产使用）。
	AllowedCommands []string

	// DeniedCommandPatterns 是禁止的命令模式（正则）。
	// 例：`curl\s*\|\s*(sh|bash|python|ruby|perl)`、`rm\s+-rf\s+/`
	DeniedCommandPatterns []*regexp.Regexp

	// NetworkCommands 是需要 URL 预检的命令列表。
	// 这些命令的参数中若包含 URL，会调用 CheckURL 预检。
	// 例：["curl", "wget"]
	NetworkCommands []string
}

// Compile 校验并归一化策略配置，返回归一化后的策略。
//
// 归一化操作：
//   - AllowedDirs：filepath.Clean，转绝对路径
//   - DeniedFileGlobs：转小写
//   - DeniedFilePaths：filepath.Clean
//   - AllowedCommands：转小写
//   - NetworkCommands：转小写
//
// 返回错误的情况：
//   - NetworkDenySubnets / NetworkAllowSubnets 中包含 nil
//
// 调用时机：NewSandbox 与 UpdatePolicy 内部自动调用，调用方无需手动调用。
func (p SandboxPolicy) Compile() (SandboxPolicy, error) {
	compiled := p

	// 归一化目录：Clean 并转绝对路径
	compiled.AllowedDirs = normalizePaths(p.AllowedDirs)
	compiled.DeniedFilePaths = normalizePaths(p.DeniedFilePaths)
	compiled.DeniedDevicePaths = normalizePaths(p.DeniedDevicePaths)

	// 归一化文件 glob：转小写
	compiled.DeniedFileGlobs = normalizeGlobs(p.DeniedFileGlobs)
	compiled.DeniedDirGlobs = normalizeGlobs(p.DeniedDirGlobs)

	// 归一化命令：转小写
	compiled.AllowedCommands = normalizeLowerStrings(p.AllowedCommands)
	compiled.NetworkCommands = normalizeLowerStrings(p.NetworkCommands)

	// 校验 IP 段非 nil（Compile 不重新解析 CIDR，调用方应已解析）
	for i, n := range compiled.NetworkDenySubnets {
		if n == nil {
			return SandboxPolicy{}, fmt.Errorf("沙箱策略校验失败: NetworkDenySubnets[%d] 为 nil", i)
		}
	}
	for i, n := range compiled.NetworkAllowSubnets {
		if n == nil {
			return SandboxPolicy{}, fmt.Errorf("沙箱策略校验失败: NetworkAllowSubnets[%d] 为 nil", i)
		}
	}

	// 校验正则模式非 nil
	for i, pat := range compiled.DeniedCommandPatterns {
		if pat == nil {
			return SandboxPolicy{}, fmt.Errorf("沙箱策略校验失败: DeniedCommandPatterns[%d] 为 nil", i)
		}
	}

	return compiled, nil
}

// normalizePaths 对路径列表做 filepath.Clean，过滤空白项。
// 相对路径保持相对（用于调试），绝对路径 Clean 后保留绝对性。
// 注意：filepath.Clean("/project/..") 会返回 "/"，这是标准库行为。
func normalizePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		// 过滤空白与纯空格项
		if strings.TrimSpace(p) == "" {
			continue
		}
		out = append(out, filepath.Clean(p))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeGlobs 把 glob 模式转小写，过滤空白项。
func normalizeGlobs(globs []string) []string {
	if len(globs) == 0 {
		return nil
	}
	out := make([]string, 0, len(globs))
	for _, g := range globs {
		if strings.TrimSpace(g) == "" {
			continue
		}
		out = append(out, strings.ToLower(g))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// normalizeLowerStrings 把字符串列表转小写，过滤空白项。
func normalizeLowerStrings(strs []string) []string {
	if len(strs) == 0 {
		return nil
	}
	out := make([]string, 0, len(strs))
	for _, s := range strs {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, strings.ToLower(s))
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
