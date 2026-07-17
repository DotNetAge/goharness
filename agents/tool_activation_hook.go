package agents

import (
	"encoding/json"

	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
)

// ToolActivationHook 监听工具执行结果并更新 ActiveToolSet。
//
// 处理两种情况：
//   - ToolSelector：解析结果中的 loaded 列表并激活对应工具
//   - Skill：解析结果中的 allowed_tools 并激活对应工具
//
// 激活发生在 After() 中，因此新激活的工具从同一 Turn 的下一轮 LLM 迭代开始可用。
//
// ActiveToolSet 是单次 Ask 级别的，因此 hook 通过 setter 在每次 Ask 开始时
// 绑定当前 ActiveToolSet，通过 getter 在 After() 中读取。
type ToolActivationHook struct {
	getActiveToolSet func() *ActiveToolSet
	setActiveToolSet func(*ActiveToolSet)
	logger           logging.Logger
}

// NewToolActivationHook 创建 ToolActivationHook。
// 返回的 hook 可通过 SetActiveToolSet 绑定当前 Ask 的 ActiveToolSet。
func NewToolActivationHook(logger logging.Logger) *ToolActivationHook {
	var current *ActiveToolSet
	return &ToolActivationHook{
		getActiveToolSet: func() *ActiveToolSet { return current },
		setActiveToolSet: func(ats *ActiveToolSet) { current = ats },
		logger:           logger,
	}
}

// SetActiveToolSet 设置当前 Ask 的 ActiveToolSet。
// 在 exec 开始时调用；Ask 结束后应传 nil 解除绑定。
func (h *ToolActivationHook) SetActiveToolSet(ats *ActiveToolSet) {
	if h.setActiveToolSet != nil {
		h.setActiveToolSet(ats)
	}
}

func (h *ToolActivationHook) Priority() int {
	// 尽早运行，使激活的工具对后续钩子（如 ActionLoggerHook）可用。
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
		h.logger.Warn("ToolActivationHook: 解析 ToolSelector 结果 JSON 失败", "error", err)
		return
	}
	if len(tsr.Loaded) > 0 {
		ats := h.getActiveToolSet()
		if ats == nil {
			return
		}
		activated := ats.Activate(tsr.Loaded)
		h.logger.Info("ToolActivationHook: 通过 ToolSelector 激活工具",
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
		h.logger.Warn("ToolActivationHook: 解析 Skill 结果 JSON 失败", "error", err)
		return
	}
	if len(sr.AllowedTools) > 0 {
		ats := h.getActiveToolSet()
		if ats == nil {
			return
		}
		activated := ats.Activate(sr.AllowedTools)
		h.logger.Info("ToolActivationHook: 通过 Skill AllowedTools 激活工具",
			"skill", sr.SkillName, "count", len(activated), "tools", activated)
	}
}
