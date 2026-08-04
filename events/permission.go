package events

// PermissionPendingData 携带当工具需要用户授权（非阻塞）时
// 发送给 UI 的工具权限详情。
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

// PermissionRequestData 是旧的阻塞式权限请求类型。
// 保留用于向后兼容（TUI 可能仍在使用）。
// Deprecated: 请改用 PermissionPendingData 配合非阻塞流程。
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
