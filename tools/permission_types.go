package tools

type PermissionBehavior string

const (
	PermissionAllow PermissionBehavior = "allow"
	PermissionDeny  PermissionBehavior = "deny"
	PermissionAsk   PermissionBehavior = "ask"
)

type PermissionResult struct {
	Behavior     PermissionBehavior `json:"behavior"`
	Message      string             `json:"message,omitempty"`
	UpdatedInput map[string]any     `json:"updated_input,omitempty"`
}
