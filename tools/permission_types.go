package tools

// PermissionBehavior describes what a permission decision is at the
// runtime level. It is intentionally distinct from the user-facing
// magic-word constants (PermissionAllow / PermissionDeny) emitted over
// the chat channel — those are the strings the UI sends; this enum is
// the internal flow control.
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
