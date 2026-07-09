package agents

import (
	"encoding/json"

	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
)

// ToolActivationHook is a ToolHook that monitors tool execution results
// and updates the ActiveToolSet accordingly.
//
// It handles two cases:
//   - ToolSelector: parses the `loaded` list from the result and activates those tools
//   - Skill: parses `allowed_tools` from the result and activates them
//
// Activation happens in After(), so the newly activated tools are available
// starting from the next LLM iteration within the same Turn.
//
// The ActiveToolSet is per-Ask, so the hook uses a getter function
// (see SetActiveToolSet) to access the current ActiveToolSet.
type ToolActivationHook struct {
	getActiveToolSet func() *ActiveToolSet
	logger           logging.Logger
}

// NewToolActivationHook creates a ToolActivationHook.
// The getter function returns the current Ask's ActiveToolSet.
func NewToolActivationHook(getter func() *ActiveToolSet, logger logging.Logger) *ToolActivationHook {
	return &ToolActivationHook{getActiveToolSet: getter, logger: logger}
}

func (h *ToolActivationHook) Priority() int {
	// Run early so activated tools are available for subsequent hooks/ActionLoggerHook.
	return 20
}

func (h *ToolActivationHook) Before(sessionID string, toolName string, params map[string]any) hooks.HookResult {
	return hooks.HookResult{}
}

func (h *ToolActivationHook) After(result *hooks.ToolResult) hooks.HookResult {
	switch result.ToolName {
	case "ToolSelector":
		h.handleToolSelector(result)
	case "Skill":
		h.handleSkill(result)
	}
	return hooks.HookResult{}
}

func (h *ToolActivationHook) Abort(reason string) {}

// ── ToolSelector handler ────────────────────────────────────────────────

type toolSelectorResult struct {
	Loaded   []string `json:"loaded"`
	NotFound []string `json:"not_found"`
	Message  string   `json:"message"`
}

func (h *ToolActivationHook) handleToolSelector(result *hooks.ToolResult) {
	if result.Result == "" {
		return
	}
	var tsr toolSelectorResult
	if err := json.Unmarshal([]byte(result.Result), &tsr); err != nil {
		h.logger.Warn("ToolActivationHook: failed to parse ToolSelector result JSON", "error", err)
		return
	}
	if len(tsr.Loaded) > 0 {
		ats := h.getActiveToolSet()
		if ats == nil {
			return
		}
		activated := ats.Activate(tsr.Loaded)
		h.logger.Info("ToolActivationHook: tools activated via ToolSelector",
			"count", len(activated), "tools", activated)
	}
}

// ── Skill handler ───────────────────────────────────────────────────────

type skillResult struct {
	SkillName    string   `json:"skill_name"`
	AllowedTools []string `json:"allowed_tools"`
}

func (h *ToolActivationHook) handleSkill(result *hooks.ToolResult) {
	if result.Result == "" {
		return
	}
	var sr skillResult
	if err := json.Unmarshal([]byte(result.Result), &sr); err != nil {
		h.logger.Warn("ToolActivationHook: failed to parse Skill result JSON", "error", err)
		return
	}
	if len(sr.AllowedTools) > 0 {
		ats := h.getActiveToolSet()
		if ats == nil {
			return
		}
		activated := ats.Activate(sr.AllowedTools)
		h.logger.Info("ToolActivationHook: tools activated via Skill AllowedTools",
			"skill", sr.SkillName, "count", len(activated), "tools", activated)
	}
}
