package reactor

import (
	"context"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goreact/core"
)

// MemoryThoughtHook implements LoopHook to inject relevant context from
// Memory into the System Prompt before each LLM call.
//
// This is the read-half of the memory closed loop. The write-half is handled
// by the MemorySlideHandler in LLMCaller.
type MemoryThoughtHook struct {
	memory core.Memory
}

func NewMemoryThoughtHook(mem core.Memory) *MemoryThoughtHook {
	return &MemoryThoughtHook{memory: mem}
}

func (h *MemoryThoughtHook) Priority() int { return 50 }

func (h *MemoryThoughtHook) BeforeLLM(sessionID string, iteration int, input *CallInput) HookResult {
	if input.UserMessage == "" || h.memory == nil {
		return HookResult{}
	}

	records, err := h.memory.Retrieve(context.Background(), input.UserMessage,
		core.WithMemoryTypes(core.MemoryTypeSession),
		core.WithMemorySessionID(sessionID),
		core.WithMinScore(0.3),
	)
	if err != nil || len(records) == 0 {
		return HookResult{}
	}

	if memContent := core.FormatMemoryRecords(records); memContent != "" {
		input.SystemPromptSections = append(
			input.SystemPromptSections,
			gochatcore.NewSystemMessage("## Relevant Context\n"+memContent),
		)
	}
	return HookResult{}
}

func (h *MemoryThoughtHook) AfterLLM(_ string, _ int, _ *LLMResponse, _ []ToolResult) HookResult {
	return HookResult{}
}

func (h *MemoryThoughtHook) Abort(_ string, _ string) {}
