package events

// PermissionPendingData carries the tool permission details sent to the UI
// when a tool requires user authorization (non-blocking).
type PermissionPendingData struct {
	TickID        string         `json:"tick_id"`
	ToolName      string         `json:"tool_name"`
	Params        map[string]any `json:"params,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	SecurityLevel SecurityLevel  `json:"security_level"`
}

func NewPermissionPendingData(tickID, toolName string, params map[string]any, reason string, secLevel SecurityLevel) PermissionPendingData {
	return PermissionPendingData{
		TickID:        tickID,
		ToolName:      toolName,
		Params:        params,
		Reason:        reason,
		SecurityLevel: secLevel,
	}
}

// PermissionRequestData is the old blocking permission request type.
// Kept for backward compatibility (TUI may still use it).
// Deprecated: Use PermissionPendingData with the non-blocking flow instead.
type PermissionRequestData struct {
	TickID        string         `json:"tick_id"`
	ToolName      string         `json:"tool_name"`
	Params        map[string]any `json:"params,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	SecurityLevel SecurityLevel  `json:"security_level"`

	grant func(updatedInput map[string]any)
	deny  func(reason string)
}

func (d *PermissionRequestData) Grant(updatedInput map[string]any) {
	if d.grant != nil {
		d.grant(updatedInput)
	}
}

func (d *PermissionRequestData) Deny(reason string) {
	if d.deny != nil {
		d.deny(reason)
	}
}

func NewPermissionRequestData(tickID, toolName string, params map[string]any, reason string, secLevel SecurityLevel, grantFn func(map[string]any), denyFn func(string)) PermissionRequestData {
	return PermissionRequestData{
		TickID:        tickID,
		ToolName:      toolName,
		Params:        params,
		Reason:        reason,
		SecurityLevel: secLevel,
		grant:         grantFn,
		deny:          denyFn,
	}
}
