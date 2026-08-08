package sandbox

import (
	"net"
	"regexp"
	"strings"
)

// 默认策略常量。所有默认值与现有 tools 包行为等价（搬家不扩展）。
//
// 行为等价对照：
//   - DeniedFileGlobs  ← tools/utils.go:checkSensitiveFiles 的 8 项
//   - NetworkDenySubnets ← tools/web_fetch.go:isPrivateIP 的 12 段（含 CGNAT 补充）
//   - AllowedCommands ← tools/bash.go:baseCmds
//   - DeniedCommandPatterns ← tools/bash.go:detectDangerousCommand
//   - NetworkCommands ← 新增（bash 的 curl/wget 需 URL 预检）

// DefaultDeniedFileGlobs 返回默认的敏感文件名 glob 模式。
// 与 tools/utils.go:checkSensitiveFiles 行为等价。
func DefaultDeniedFileGlobs() []string {
	return []string{
		".env",
		"id_rsa",
		"id_ed25519",
		"passwd",
		"shadow",
		"sudoers",
		".ssh_config",
		"known_hosts",
	}
}

// DefaultDeniedDirGlobs 返回默认的敏感目录名 glob 模式。
// 命中路径中任意一段即拒绝。
// 这是设计文档第 7.1 节定义的扩展（现有 tools 包未实现，沙箱新增）。
func DefaultDeniedDirGlobs() []string {
	return []string{
		".ssh",
		".aws",
		".docker",
		".kube",
		".gnupg",
		".config",
	}
}

// DefaultDeniedDevicePaths 返回默认的设备文件路径黑名单。
// 与 tools/read_validate.go:deviceFileBlacklist 行为等价。
// 读取这些文件会导致进程挂起或输出无意义内容。
func DefaultDeniedDevicePaths() []string {
	return []string{
		"/dev/zero",
		"/dev/random",
		"/dev/urandom",
		"/dev/null",
		"/dev/tty",
		"/dev/stdin",
		"/dev/stdout",
		"/dev/stderr",
		"/dev/fd/0",
		"/dev/fd/1",
		"/dev/fd/2",
		"/proc/self/fd/0",
		"/proc/self/fd/1",
		"/proc/self/fd/2",
	}
}

// DefaultDeniedSubnets 返回默认禁止访问的 IP 段。
// 与 tools/web_fetch.go:isPrivateIP 行为等价（含 CGNAT 补充）。
func DefaultDeniedSubnets() []*net.IPNet {
	return parseCIDRs([]string{
		"127.0.0.0/8",    // 回环
		"10.0.0.0/8",     // 私有 A
		"172.16.0.0/12",  // 私有 B
		"192.168.0.0/16", // 私有 C
		"169.254.0.0/16", // 链路本地（AWS/GCP 元数据）
		"100.64.0.0/10",  // CGNAT（含阿里云元数据 100.100.100.200）
		"192.0.0.0/24",   // IETF 协议分配
		"198.18.0.0/15",  // 基准测试网络
		"::1/128",        // IPv6 回环
		"fc00::/7",       // IPv6 ULA
		"fe80::/10",      // IPv6 链路本地
		"0.0.0.0/8",      // 未指定网络
	})
}

// DefaultAllowedCommands 返回默认允许的命令白名单。
// 与 tools/bash.go:baseCmds 行为等价（直接搬家，保持一致）。
func DefaultAllowedCommands() []string {
	return []string{
		"cat", "echo", "head", "tail", "less", "more",
		"ls", "wc", "pwd", "cd", "mkdir", "touch", "cp", "mv", "rm",
		"chmod", "chown", "ln", "tar", "gzip", "gunzip", "zip", "unzip",
		"git", "svn", "hg",
		"python", "python3", "pip", "pip3", "node", "npm", "cnpm", "npx", "nvm",
		"go", "cargo", "rustc",
		"make", "cmake", "gcc", "g++", "clang", "clang++",
		"docker", "docker-compose", "kubectl", "helm",
		"curl", "wget", "ssh", "scp", "rsync",
		"ps", "top", "htop", "kill", "killall", "pgrep", "pkill",
		"df", "du", "free", "uname", "date", "whoami", "id", "mindx", "mrag",
		"env", "export", "source", "alias", "which", "type", "file",
		"sort", "uniq", "cut", "tr", "tee", "xargs",
		"grep", "rg", "find", "diff", "comm",
		"awk", "sed", "printf",
		"jq", "yq",
		"test", "[[", "true", "false", "exit", "return",
		// Shell 语法关键字（非外部命令，不会产生进程）
		"for", "while", "until", "if", "then", "else", "elif", "fi",
		"case", "esac", "do", "done", "select", "function",
		"sleep", "wait", "bg", "fg", "jobs", "nohup", "disown",
		"basename", "dirname", "realpath", "readlink",
		"sha256sum", "md5sum", "sha1sum", "shasum",
		"openssl", "gpg", "ssh-keygen",
		"time", "timeout", "watch",
		"hostname", "uptime", "lscpu", "lsblk",
		"ping", "ss", "dig", "host", "nslookup", "traceroute", "ip",
		"lsof",
		"strings", "xxd", "od", "column", "seq", "shuf", "fmt", "nl", "fold",
		"ldd", "nm", "objdump", "readelf", "size", "strip",
	}
}

// DefaultDeniedCommandPatterns 返回默认的危险命令正则模式。
// 与 tools/bash.go:dangerousPatterns 行为等价（完整搬家，保持一致）。
func DefaultDeniedCommandPatterns() []*regexp.Regexp {
	return compilePatterns([]string{
		`^rm\s+-rf\s+/\s*$`,
		`^rm\s+-rf\s+/\*\s*$`,
		`>\s*/dev/sd[a-z]\b`,
		`dd\s+if=.*of=/dev/sd`,
		`mkfs\.`,
		`:\(\)\{\s*\|.*:\s*&\s*;:\s*\}$`,
		`(curl|wget)\s+.*\|\s*(sh|bash)\b`,
		`(curl|wget)\s+.*\s*>\s*/(bin|usr/bin)/`,
		`chmod\s+-R\s+777\s+/`,
		`chown\s+-R.*\s+/`,
		`shutdown\s+(now|-h|-r)\b`,
		`^reboot\s*$`,
		`(?i)format\s+[a-z]:\s*/q`,
		`(?i)del\s+/[fs]\s+.*\\\*\.\*`,
		`(?i)rd\s+/[fs]\s+\\`,
		`(?i)reg\s+delete\s+HK`,
		`(?i)diskpart`,
		`(?i)icacls\s+.*\s+/grant\s+.*:F`,
		`(?i)takeown\s+/f`,
		`(?i)cipher\s+/w:`,
		`(?i)bcdedit\s+/set`,
		`(?i)powercfg\s+/h\s+off`,
		`(?i)net\s+user\s+.*\s+/add`,
		`(?i)net\s+localgroup\s+.*\s+/add`,
		`(?i)sc\s+delete`,
	})
}

// DefaultNetworkCommands 返回默认需要 URL 预检的命令列表。
// 这些命令的参数中若包含 URL，会调用 CheckURL 预检。
func DefaultNetworkCommands() []string {
	return []string{"curl", "wget"}
}

// parseCIDRs 把 CIDR 字符串列表解析为 *net.IPNet 列表。
// 解析失败的项被跳过（不阻断初始化）。
func parseCIDRs(cidrs []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		if n := parseCIDR(cidr); n != nil {
			out = append(out, n)
		}
	}
	return out
}

// parseCIDR 解析单个 CIDR 字符串。
func parseCIDR(s string) *net.IPNet {
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		return nil
	}
	return n
}

// compilePatterns 编译正则表达式列表。
// 编译失败的项被跳过（不阻断初始化）。
func compilePatterns(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err == nil {
			out = append(out, re)
		}
	}
	return out
}

// containsLower 在小写化的列表中查找小写化后的项。
func containsLower(list []string, s string) bool {
	target := strings.ToLower(s)
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
