package tools

// PermissionBehavior 描述运行时层面的权限决策语义。
// 它与用户可见的魔法词常量（PermissionAllow / PermissionDeny）有意区分——
// 魔法词是 UI 发送到聊天通道的字符串，而此枚举用于内部流程控制。
type PermissionBehavior string

const (
	PermissionBehaviorAllow PermissionBehavior = "allow"
	PermissionBehaviorDeny  PermissionBehavior = "deny"
	PermissionBehaviorAsk   PermissionBehavior = "ask"
)

type PermissionResult struct {
	Behavior     PermissionBehavior `json:"behavior"`
	Message      string             `json:"message,omitempty"`
	UpdatedInput map[string]any     `json:"updated_input,omitempty"`
}
