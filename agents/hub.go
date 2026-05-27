package agents

import (
	"github.com/DotNetAge/goreact/config"
	"github.com/DotNetAge/goreact/rule"
	"github.com/DotNetAge/goreact/skill"
	"github.com/DotNetAge/goreact/tools"
)

// RegistryHub provides access to all registries held by the Runtime.
// This is the port of reactor/hub.go with ProviderRegistry added.
type RegistryHub interface {
	SkillRegistry() skill.SkillRegistry
	ToolRegistry() tools.ToolRegistry
	ToolExecutor() tools.ToolExecutor
	RuleRegistry() rule.RuleRegistry
	ProviderRegistry() config.ProviderRegistry
	RegisterTool(tool tools.FuncTool) error
}

var _ RegistryHub = (*Runtime)(nil)
