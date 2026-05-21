package reactor

import (
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goreact/core"
)

// ────────────────────────────────────────────────────────────────────────────
// MemoryThoughtHook — 记忆闭环的"读半环"
// ────────────────────────────────────────────────────────────────────────────

// MemoryThoughtHook implements ThoughtHook to inject relevant context from
// Memory into the System Prompt before each Think phase.
//
// This is the read-half of the memory closed loop. The write-half is handled
// by the MemorySlideHandler in doSlide(), which stores slid-out context
// window messages as MemoryRecords via the SlideHandler callback.
//
// Architecture:
//
//	doSlide() → SlideHandler → Memory.Store()   ← write-half (MemorySlideHandler)
//	Before()  → Memory.Retrieve() → SystemPrompt  ← read-half  (MemoryThoughtHook)
type MemoryThoughtHook struct {
	memory core.Memory
}

// NewMemoryThoughtHook creates a MemoryThoughtHook that retrieves relevant
// context from the given Memory and injects it into the System Prompt as a
// "## Relevant Context" section (after the KV Cache boundary).
func NewMemoryThoughtHook(mem core.Memory) *MemoryThoughtHook {
	return &MemoryThoughtHook{memory: mem}
}

// Priority returns 50 — runs after all user hooks (0-39) and other built-in
// hooks (40-49) but before convergence check (49) in the thought chain.
// This ensures memory context is available during the LLM call.
func (h *MemoryThoughtHook) Priority() int { return 50 }

// Before retrieves relevant context from Memory and injects it into the
// SystemPromptSections. The injection happens after the KV Cache boundary
// (dynamic zone), so it does not invalidate the KV cache prefix.
func (h *MemoryThoughtHook) Before(ctx *ReactContext, input *CallInput) HookResult {
	if ctx.Input == "" || h.memory == nil {
		return HookResult{}
	}

	records, err := h.memory.Retrieve(ctx.Ctx(), ctx.Input,
		core.WithMemoryTypes(core.MemoryTypeSession),
		core.WithMemorySessionID(ctx.SessionID),
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

// After is a no-op — memory does not need post-thought processing.
func (h *MemoryThoughtHook) After(_ *ReactContext, _ *Thought) HookResult {
	return HookResult{}
}

// Abort is a no-op — memory does not need abort cleanup.
func (h *MemoryThoughtHook) Abort(_ *ReactContext, _ string) {}
