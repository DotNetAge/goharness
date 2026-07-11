package loop

import (
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/memory"
)

// MemoryThoughtHook retrieves relevant memory records before each LLM call
// and injects them as context into the system prompt.
//
// Two retrievals are performed:
//  1. Current-session memory: scoped by SessionID for conversation continuity
//  2. Cross-session memory: scoped by AgentName for long-term knowledge
//
// Both are injected as separate sections so the LLM can distinguish context sources.
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

	added := false

	// 1. Session-scoped retrieval (current conversation)
	sessionRecords, err := h.memory.Retrieve(nil, input.UserMessage,
		memory.WithMemorySessionID(sessionID),
		memory.WithMinScore(0.3),
	)
	if err == nil && len(sessionRecords) > 0 {
		if content := memory.FormatMemoryRecords(sessionRecords); content != "" {
			input.SystemPromptSections = append(
				input.SystemPromptSections,
				gochatcore.NewSystemMessage(
					"## 相关记忆\n"+
						"以下是当前对话的相关记忆，可能对你的推理有参考作用。\n"+
						content,
				),
			)
			added = true
		}
	}

	// 2. Cross-session retrieval (other sessions for this agent)
	if input.AgentName != "" {
		crossRecords, err := h.memory.Retrieve(nil, input.UserMessage,
			memory.WithAgentName(input.AgentName),
			memory.WithMinScore(0.3),
		)
		if err == nil && len(crossRecords) > 0 {
			// Deduplicate: skip chunks already shown in session context
			seen := make(map[string]struct{}, len(sessionRecords))
			for _, r := range sessionRecords {
				seen[r.ID] = struct{}{}
			}
			filtered := make([]memory.MemoryChunk, 0, len(crossRecords))
			for _, r := range crossRecords {
				if _, dup := seen[r.ID]; !dup {
					filtered = append(filtered, r)
				}
			}
			if len(filtered) > 0 {
				if content := memory.FormatMemoryRecords(filtered); content != "" {
					input.SystemPromptSections = append(
						input.SystemPromptSections,
						gochatcore.NewSystemMessage(
							"## 其他会话的相关内容\n"+
								"以下是我在其他会话中与当前对话相关的记忆。"+
								"它们可能包含一些有帮助性的内容——请根据你的判断决定其适用性:\n\n"+
								content,
						),
					)
					added = true
				}
			}
		}
	}

	// Fallback: if only one retrieval succeeded, inject as generic context
	if !added && len(sessionRecords) > 0 {
		if content := memory.FormatMemoryRecords(sessionRecords); content != "" {
			input.SystemPromptSections = append(
				input.SystemPromptSections,
				gochatcore.NewSystemMessage(
					"## 相关上下文\n"+
						"以下是相关的历史记忆:\n\n"+
						content,
				),
			)
		}
	}
	return hooks.HookResult{}
}

// AfterLLM is a no-op for MemoryThoughtHook.
func (h *MemoryThoughtHook) AfterLLM(_ string, _ int, _ *hooks.LLMResponse, _ []hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

// Abort is a no-op for MemoryThoughtHook.
func (h *MemoryThoughtHook) Abort(_ string, _ string) {}
