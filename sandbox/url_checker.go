package sandbox

import (
	"net"
	"net/url"
	"strings"
)

// CheckURL 检查 URL 是否允许访问。
//
// 决策流程：
//  1. 解析 URL 与 host
//  2. 对域名做 DNS 解析（防域名指向私有 IP）
//  3. 对每个解析到的 IP 检查是否在禁止网段
//  4. 若命中禁止网段且未在允许列表中 → Deny
//
// 已知局限：
//   - DNS 解析与实际请求之间存在时间窗口，理论上可被 DNS rebinding 绕过
//   - 不拦截基于域名的访问（如 attacker.com 解析到公网 IP 时通过）
//   - 拨号层强制拦截需要 hook http.Transport.DialContext，本方案未实现
func (s *Sandbox) CheckURL(rawURL string) URLDecision {
	p := s.policy.Load()

	u, err := url.Parse(rawURL)
	if err != nil {
		return URLDecision{
			Decision: DecisionDeny,
			Reason:   GuideURLParseFailed(rawURL, err.Error()),
		}
	}

	host := u.Hostname()
	if host == "" {
		return URLDecision{
			Decision: DecisionDeny,
			Reason:   GuideURLParseFailed(rawURL, "URL 中缺少 host"),
		}
	}

	// 解析 host 为 IP 列表（域名做 DNS 解析，IP 直接解析）
	ips, err := resolveHostIPs(host)
	if err != nil || len(ips) == 0 {
		return URLDecision{
			Decision: DecisionDeny,
			Reason:   GuideURLParseFailed(rawURL, "主机名解析失败"),
		}
	}

	// 对每个 IP 检查
	for _, ip := range ips {
		if s.isIPDenied(ip, p) {
			return URLDecision{
				Decision:    DecisionDeny,
				Reason:      GuideSSRFBlocked(rawURL, ips),
				ResolvedIPs: ips,
			}
		}
	}

	return URLDecision{
		Decision:    DecisionAllow,
		ResolvedIPs: ips,
	}
}

// isIPDenied 检查 IP 是否在禁止网段且未在允许列表中。
// 允许列表优先于禁止列表（用于放行特定内网服务）。
func (s *Sandbox) isIPDenied(ipStr string, p *SandboxPolicy) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		// 无法解析为 IP → 视为可疑，拒绝
		return true
	}

	// 先查允许列表
	for _, n := range p.NetworkAllowSubnets {
		if n != nil && n.Contains(ip) {
			return false
		}
	}

	// 再查禁止列表
	for _, n := range p.NetworkDenySubnets {
		if n != nil && n.Contains(ip) {
			return true
		}
	}

	return false
}

// resolveHostIPs 把 host 解析为 IP 字符串列表。
// 若 host 本身是 IP，直接返回；若是域名，做 DNS 解析。
func resolveHostIPs(host string) ([]string, error) {
	// 先尝试直接解析为 IP
	if ip := net.ParseIP(host); ip != nil {
		return []string{ip.String()}, nil
	}

	// 域名解析（IPv4 + IPv6）
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out, nil
}

// extractURLsFromCommand 从命令字符串中提取 URL。
// 使用 urlPatternCache 编译好的正则，匹配 http:// 和 https:// 开头的 URL。
//
// 已知局限：
//   - 无法解析变量替换（URL=$X && curl $X）
//   - 无法解析命令替换（curl $(cat file)）
//   - 这些场景会绕过 URL 预检，是逻辑沙箱的固有限制
func extractURLsFromCommand(command string) []string {
	return extractByPattern(command, "")
}

// ExtractURLsFromCommand 是 extractURLsFromCommand 的导出版本，
// 供 tools 包在沙箱启用时主动提取命令中的 URL 做预检（如会话白名单命中后的 SSRF 防护）。
func ExtractURLsFromCommand(command string) []string {
	return extractURLsFromCommand(command)
}

// extractByPattern 用正则提取所有匹配项。
func extractByPattern(s, pattern string) []string {
	// 延迟编译，避免包初始化时编译所有正则
	re := urlPatternMust(pattern)
	matches := re.FindAllString(s, -1)
	if len(matches) == 0 {
		return nil
	}
	// 去重
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			out = append(out, strings.TrimRight(m, ".,;:!?"))
		}
	}
	return out
}
