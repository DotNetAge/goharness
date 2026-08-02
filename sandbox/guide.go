package sandbox

import (
	"fmt"
	"strings"
)

// Guide 风格说明：与 tools/guide.go 保持一致的第一人称引导式错误信息。
// 设计原则：action（我做了什么）+ cause（为什么被拦）+ next（下一步建议）。
// 沙箱独立实现，不依赖 tools 包，避免循环依赖。

// buildGuide 组装第一人称引导式错误消息。
func buildGuide(action, cause, next string) string {
	return fmt.Sprintf("我%s。\n原因：%s。\n下一步我应该：%s", action, cause, next)
}

// GuideSensitiveFile 构造访问敏感文件被拒的引导信息。
func GuideSensitiveFile(path string) string {
	return buildGuide(
		fmt.Sprintf("尝试访问 %q", path),
		"该文件位于沙箱敏感文件黑名单中，属于硬性安全边界，授权不可覆盖",
		"避免访问凭证、密钥与系统配置文件；若确实需要审计，请改用专用审计 Agent（其沙箱策略允许敏感文件）",
	)
}

// GuideDeviceFile 构造访问设备文件被拒的引导信息。
// 设备文件（如 /dev/zero、/dev/random）读取会导致进程挂起或输出无意义内容。
func GuideDeviceFile(path string) string {
	return buildGuide(
		fmt.Sprintf("尝试访问 %q", path),
		"该路径是设备文件，读取会导致进程挂起或输出无意义内容",
		"避免读取 /dev/* 与 /proc/self/fd/* 路径；若确实需要读取设备输出，应使用专用命令（如 head -c 限制字节数）",
	)
}

// GuideOutsideWorkspace 构造访问工作区外文件被拒的引导信息。
func GuideOutsideWorkspace(path, projectDir string) string {
	return buildGuide(
		fmt.Sprintf("尝试访问 %q", path),
		fmt.Sprintf("该路径位于工作区 %q 之外", projectDir),
		"先自查路径是否拼写错误；若确实需要访问工作区外文件，应询问用户授权（Allow / AllowSession）",
	)
}

// GuideSSRFBlocked 构造 URL 被 SSRF 策略拦截的引导信息。
func GuideSSRFBlocked(rawURL string, ips []string) string {
	ipStr := "未知"
	if len(ips) > 0 {
		ipStr = strings.Join(ips, ", ")
	}
	return buildGuide(
		fmt.Sprintf("尝试访问 URL %q", rawURL),
		fmt.Sprintf("目标主机解析到 IP %s，位于沙箱禁止访问的网段（私有 IP / 云元数据 / CGNAT 等）", ipStr),
		"避免访问内网或云元数据端点；若确实需要访问内网服务，请让宿主通过 NetworkAllowSubnets 配置显式放行",
	)
}

// GuideCommandDenied 构造命令被拒绝的引导信息。
func GuideCommandDenied(command, reason string) string {
	return buildGuide(
		fmt.Sprintf("尝试执行命令 %q", command),
		reason,
		"改用白名单内的命令；若确实需要执行被拒命令，应询问用户授权或让宿主通过 AllowedCommands 配置放行",
	)
}

// GuideDangerousPattern 构造命令匹配危险模式的引导信息。
func GuideDangerousPattern(command, pattern string) string {
	return buildGuide(
		fmt.Sprintf("尝试执行命令 %q", command),
		fmt.Sprintf("命令匹配危险模式 %q", pattern),
		"避免使用远程执行、格式化、写裸设备等危险操作；改用受控的本地命令完成相同任务",
	)
}

// GuideURLParseFailed 构造 URL 解析失败的引导信息。
func GuideURLParseFailed(rawURL, cause string) string {
	return buildGuide(
		fmt.Sprintf("尝试解析 URL %q", rawURL),
		"URL 解析失败："+cause,
		"先自查 URL 格式是否正确（含 scheme、host）；若 URL 来自变量替换，应在命令中显式写出 URL",
	)
}
