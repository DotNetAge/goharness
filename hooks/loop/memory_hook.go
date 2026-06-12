package loop

import (
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/memory"
)

// MemoryThoughtHook retrieves relevant memory records before each LLM call
// and injects them as context into the system prompt.
type MemoryThoughtHook struct {
	memory memory.Memory
}

// NewMemoryThoughtHook creates a new MemoryThoughtHook with the given memory store.
func NewMemoryThoughtHook(mem memory.Memory) *MemoryThoughtHook {
	return &MemoryThoughtHook{memory: mem}
}

// Priority returns the priority for MemoryThoughtHook (50).
func (h *MemoryThoughtHook) Priority() int { return 50 }

// BeforeLLM retrieves relevant memory records for the current input
// and injects them as a system prompt section.
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

// AfterLLM is a no-op for MemoryThoughtHook.
func (h *MemoryThoughtHook) AfterLLM(_ string, _ int, _ *hooks.LLMResponse, _ []hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

// Abort is a no-op for MemoryThoughtHook.
func (h *MemoryThoughtHook) Abort(_ string, _ string) {}
