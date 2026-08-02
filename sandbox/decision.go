// Package sandbox 提供 GoHarness 的会话级逻辑沙箱。
//
// 沙箱是"决策层"而非"执行环境"——它不创建子进程、不改写系统视图、不操作系统调用，
// 只在工具的 Grant/Execute 拦截点做应用层决策（Allow/Deny/AskUser）。
//
// 这是逻辑沙箱：跨平台、零部署成本、无性能开销，代价是应用层解析可能被绕过。
// 详见 SANDBOX-RAISON-ETRE.md 与 SANDBOX-DESIGN.md。
package sandbox

// Decision 是沙箱对一个操作的基础决策结果。
type Decision int

const (
	// DecisionAllow 允许执行，无需询问用户。
	DecisionAllow Decision = iota

	// DecisionDeny 硬性禁止，授权不可覆盖。用于敏感文件、危险命令、SSRF 目标。
	DecisionDeny

	// DecisionAskUser 需要询问用户授权。用于目录边界外的非敏感文件、非白名单命令。
	DecisionAskUser
)

// String 返回决策的可读名称，用于日志与错误信息。
func (d Decision) String() string {
	switch d {
	case DecisionAllow:
		return "允许"
	case DecisionDeny:
		return "拒绝"
	case DecisionAskUser:
		return "询问用户"
	default:
		return "未知"
	}
}

// FileDecision 是文件操作的决策结果。
type FileDecision struct {
	Decision Decision
	// Reason 是决策原因，用于错误信息或授权提示。
	Reason string
}

// URLDecision 是网络访问的决策结果。
type URLDecision struct {
	Decision Decision
	// Reason 是决策原因。
	Reason string
	// ResolvedIPs 是 URL 解析到的 IP 地址，用于日志审计。
	ResolvedIPs []string
}

// CommandDecision 是命令执行的决策结果。
type CommandDecision struct {
	Decision Decision
	// Reason 是决策原因。
	Reason string
	// NeedURLCheck 表示该命令包含 URL 参数，需要进一步 CheckURL。
	// bash 工具在 Grant 阶段对 curl/wget 等命令做 URL 预检。
	NeedURLCheck bool
	// URLs 是从命令中提取的 URL，当 NeedURLCheck=true 时填充。
	URLs []string
}
