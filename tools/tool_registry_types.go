package tools

import "github.com/DotNetAge/goreact/events"

// ToolRegistry manages the registration and discovery of FuncTool instances.
// This is a DYNAMIC registry: tools can be registered/unregistered at runtime
// based on context (e.g., permission level, active skills).
type ToolRegistry interface {
	Register(tool FuncTool) error
	Get(name string) (FuncTool, bool)
	All() []FuncTool
	FindAvailable(filter *ToolFilter) []FuncTool
	// Remove deletes a tool from the registry by name. Returns an error if the
	// tool is not found.
	Remove(name string) error
}

type ToolFilter struct {
	Terms        string               // semantic matching terms
	Security     events.SecurityLevel // matching security level
	Keywords     []string             // matching keywords
	AllowedNames []string             // matching tool names
}
