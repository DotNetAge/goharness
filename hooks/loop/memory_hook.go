package loop

import (
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goreact/hooks"
	"github.com/DotNetAge/goreact/memory"
)

type MemoryThoughtHook struct {
	memory memory.Memory
}

func NewMemoryThoughtHook(mem memory.Memory) *MemoryThoughtHook {
	return &MemoryThoughtHook{memory: mem}
}

func (h *MemoryThoughtHook) Priority() int { return 50 }

func (h *MemoryThoughtHook) BeforeLLM(sessionID string, iteration int, input *hooks.CallInput) hooks.HookResult {
	if input.UserMessage == "" || h.memory == nil {
		return hooks.HookResult{}
	}

	records, err := h.memory.Retrieve(nil, input.UserMessage,
		memory.WithMemoryTypes(memory.MemoryTypeSession),
		memory.WithMemorySessionID(sessionID),
		memory.WithMinScore(0.3),
	)
	if err != nil || len(records) == 0 {
		return hooks.HookResult{}
	}

	if memContent := memory.FormatMemoryRecords(records); memContent != "" {
		input.SystemPromptSections = append(
			input.SystemPromptSections,
			gochatcore.NewSystemMessage("## Relevant Context\n"+memContent),
		)
	}
	return hooks.HookResult{}
}

func (h *MemoryThoughtHook) AfterLLM(_ string, _ int, _ *hooks.LLMResponse, _ []hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

func (h *MemoryThoughtHook) Abort(_ string, _ string) {}
