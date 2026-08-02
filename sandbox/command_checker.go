package sandbox

import (
	"path/filepath"
	"regexp"
	"strings"
)

// CheckCommand 在 Grant 阶段调用，检查命令是否允许执行。
//
// 决策流程：
//  1. 危险命令模式检测 → Deny
//  2. 白名单检测 → 不在白名单则 AskUser
//  3. 网络命令 URL 预检 → 提取 URL 返回 NeedURLCheck=true
//  4. 其它 → Allow
//
// bash 工具在 Grant 阶段调用此方法，并根据 NeedURLCheck 决定是否进一步 CheckURL。
func (s *Sandbox) CheckCommand(command string) CommandDecision {
	p := s.policy.Load()

	// 1. 危险命令模式检测（硬性拒绝）
	for _, pattern := range p.DeniedCommandPatterns {
		if pattern != nil && pattern.MatchString(command) {
			return CommandDecision{
				Decision: DecisionDeny,
				Reason:   GuideDangerousPattern(command, pattern.String()),
			}
		}
	}

	// 2. 白名单检测
	baseCmd := extractBaseCommand(command)
	if len(p.AllowedCommands) > 0 && !containsLower(p.AllowedCommands, baseCmd) {
		return CommandDecision{
			Decision: DecisionAskUser,
			Reason:   GuideCommandDenied(command, "命令 "+baseCmd+" 不在白名单中"),
		}
	}

	// 3. 网络命令 URL 预检
	if containsLower(p.NetworkCommands, baseCmd) {
		urls := extractURLsFromCommand(command)
		if len(urls) > 0 {
			return CommandDecision{
				Decision:     DecisionAllow,
				NeedURLCheck: true,
				URLs:         urls,
			}
		}
	}

	return CommandDecision{Decision: DecisionAllow}
}

// extractBaseCommand 从命令字符串中提取基础命令名（小写）。
//
// 处理：
//   - 去除前导空白与环境变量赋值（如 FOO=bar curl ...）
//   - 去除 sudo 前缀
//   - 取第一个 token 的 basename
//   - 转小写
//
// 不处理：
//   - 管道与命令分隔（curl x | sh 中的 sh 不会被提取，但管道是危险模式应被拦截）
//   - shell 别名与函数
func extractBaseCommand(command string) string {
	s := strings.TrimSpace(command)
	if s == "" {
		return ""
	}

	// 去除前导环境变量赋值：KEY=val KEY2=val2 cmd ...
	for {
		if eq := strings.Index(s, "="); eq > 0 {
			// 检查 = 前是否是合法变量名（字母数字下划线）
			keyPart := s[:eq]
			if isValidVarName(keyPart) {
				// 跳过 KEY=val，找下一个空白后的内容
				rest := s[eq+1:]
				// 跳过值部分（到下一个空白）
				nextSpace := strings.IndexAny(rest, " \t")
				if nextSpace < 0 {
					return "" // 只有赋值没有命令
				}
				s = strings.TrimSpace(rest[nextSpace:])
				continue
			}
		}
		break
	}

	if s == "" {
		return ""
	}

	// 取第一个 token
	endIdx := strings.IndexAny(s, " \t")
	first := s
	if endIdx > 0 {
		first = s[:endIdx]
	}

	// 去除 sudo 前缀
	if strings.ToLower(first) == "sudo" {
		// 找 sudo 之后的空白位置
		rest := ""
		if endIdx > 0 && endIdx < len(s) {
			rest = strings.TrimSpace(s[endIdx:])
		}
		if rest == "" {
			return "sudo"
		}
		endIdx2 := strings.IndexAny(rest, " \t")
		first = rest
		if endIdx2 > 0 {
			first = rest[:endIdx2]
		}
	}

	// 取 basename（处理 /usr/bin/curl 这种路径）
	first = filepath.Base(first)
	return strings.ToLower(first)
}

// isValidVarName 检查字符串是否是合法的 shell 变量名。
// 首字符必须是字母或下划线，后续可以是字母数字下划线。
func isValidVarName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_') {
				return false
			}
		} else {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r == '_') {
				return false
			}
		}
	}
	return true
}

// urlPatternCache 缓存编译后的 URL 正则，避免重复编译。
// 终止符设计：
//   - 空白、引号、|、<、>、(、)：shell 语法分隔符，URL 必须在此终止
//   - ;：shell 命令分隔符（如 "curl http://x; rm -rf /"），终止以防吞掉后续命令
//   - 保留 & = ? / : 等字符：URL 查询参数合法组成部分
var urlPatternCache = regexp.MustCompile(`https?://[^\s'"|;<>()]+`)

// urlPatternMust 返回 URL 提取正则（编译一次缓存）。
func urlPatternMust(_ string) *regexp.Regexp {
	return urlPatternCache
}
