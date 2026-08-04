package tools

import "github.com/DotNetAge/goharness/events"

// ToolRegistry 管理 FuncTool 实例的注册与发现。
// 这是一个动态注册表：可根据上下文（如权限级别、已激活技能）在运行时注册/注销工具。
type ToolRegistry interface {
	Register(tool FuncTool) error
	Get(name string) (FuncTool, bool)
	All() []FuncTool
	FindAvailable(filter *ToolFilter) []FuncTool
	// Remove 按名称从注册表删除工具。若工具不存在则返回错误。
	Remove(name string) error
}

type ToolFilter struct {
	Terms        string               // 语义匹配词
	Security     events.SecurityLevel // 匹配安全级别
	Keywords     []string             // 匹配关键词
	AllowedNames []string             // 匹配工具名
}
