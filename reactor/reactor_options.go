package reactor

import (
	"github.com/DotNetAge/goreact/core"
)

// ReactorOption configures a Reactor during creation.
type ReactorOption func(*reactorSetup)

// WithExtraTools adds additional tools to the reactor beyond the bundled ones.
func WithExtraTools(tools ...core.FuncTool) ReactorOption {
	return func(s *reactorSetup) {
		s.extraTools = append(s.extraTools, tools...)
	}
}

// WithoutBundledTools skips registration of all built-in tools (orchestration tools are still registered).
func WithoutBundledTools() ReactorOption {
	return func(s *reactorSetup) {
		s.skipAllBundled = true
	}
}

// WithoutTool skips registration of a specific built-in tool by name.
func WithoutTool(name string) ReactorOption {
	return func(s *reactorSetup) {
		if s.skipTools == nil {
			s.skipTools = make(map[string]bool)
		}
		s.skipTools[name] = true
	}
}

// WithExcludeTools removes tools from the ToolRegistry by name after all
// tools have been registered. Unlike WithoutTool (which prevents registration),
// this removes already-registered tools — useful for excluding tools loaded
// from skills, MCP servers, or extra tool lists.
func WithExcludeTools(names ...string) ReactorOption {
	return func(s *reactorSetup) {
		s.excludeTools = append(s.excludeTools, names...)
	}
}

// WithResultLimits configures tool result size thresholds (second layer defense).
func WithResultLimits(limits core.ToolResultLimits) ReactorOption {
	return func(s *reactorSetup) {
		s.resultLimits = limits
	}
}

// WithTokenEstimator sets a custom token estimator for budget tracking.
func WithTokenEstimator(estimator core.TokenEstimator) ReactorOption {
	return func(s *reactorSetup) {
		s.tokenEstimator = estimator
	}
}

// WithEventBus sets the event bus for streaming agent events.
// If not set, a new InProcessEventBus is created automatically.
func WithEventBus(bus EventBus) ReactorOption {
	return func(s *reactorSetup) {
		s.eventBus = bus
	}
}

// WithSkillDir specifies external directories to load skills from.
// Each directory should contain subdirectories, each with a SKILL.md file.
// Skills loaded from these directories are registered in addition to bundled skills.
// Multiple directories can be specified by calling WithSkillDir multiple times.
func WithSkillDir(dir string) ReactorOption {
	return func(s *reactorSetup) {
		s.skillDirs = append(s.skillDirs, dir)
	}
}

// WithSkills specifies which skills to load. If empty, all skills are loaded.
// If specified, only skills with matching names will be loaded.
// This applies to both bundled skills and skills loaded from skill directories.
func WithSkills(skillNames ...string) ReactorOption {
	return func(s *reactorSetup) {
		s.skills = append(s.skills, skillNames...)
	}
}

// WithMemory sets the primary (long-term) Memory implementation for knowledge retrieval.
// Memory is queried during the Think phase to inject relevant knowledge
// into the LLM prompt, suppressing hallucination.
// If not set, the reactor operates without memory augmentation.
func WithMemory(mem core.Memory) ReactorOption {
	return func(s *reactorSetup) {
		s.memory = mem
	}
}

// WithSessionMemory sets a separate short-term/session-scoped Memory instance.
// Unlike WithMemory (which holds long-term/project knowledge), this memory is
// used by the memory closed loop:
//   - MemorySlideHandler stores slid-out context messages here (write-half)
//   - MemoryThoughtHook retrieves from here before each Think phase (read-half)
//
// When set, the closed loop uses this instead of the primary Memory.
// Typically backed by a session-scoped RAG index (SessionRAG).
//
// Usage:
//
//	// Long-term project memory
//	opts = append(opts, goreact.WithMemory(longTermMem))
//	// Session-scoped memory for context recall
//	opts = append(opts, goreact.WithSessionMemory(sessionMem))
func WithSessionMemory(mem core.Memory) ReactorOption {
	return func(s *reactorSetup) {
		s.sessionMemory = mem
	}
}

// WithMockLLM replaces the real LLM client with a deterministic mock function.
// This is intended for end-to-end testing without requiring real API keys or network access.
// The mock function receives the full prompt context via CallInput and must return a
// complete LLM response.
func WithMockLLM(fn MockLLMFunc) ReactorOption {
	return func(s *reactorSetup) {
		s.mockLLM = fn
	}
}

// WithSystemPrompt sets a custom system prompt string for the reactor.
// This is the legacy approach — prefer WithPrompt() which uses the structured Prompt type.
// If both WithSystemPrompt and WithPrompt are set, WithPrompt takes precedence.
func WithSystemPrompt(prompt string) ReactorOption {
	return func(rs *reactorSetup) {
		rs.systemPrompt = prompt
	}
}

// WithPrompt sets a centralized Prompt struct for system prompt generation.
// This replaces the older systemPrompt string approach — the Prompt struct
// provides structured sections that remain stable across rounds (KV cache friendly).
func WithPrompt(p *Prompt) ReactorOption {
	return func(rs *reactorSetup) {
		rs.prompt = p
	}
}

// --- Registry Injection Options ---

// WithToolRegistry sets a custom ToolRegistry implementation.
// Use this to add dynamic tool discovery, semantic filtering, etc.
// If not set, DefaultToolRegistry is used automatically.
func WithToolRegistry(reg core.ToolRegistry) ReactorOption {
	return func(s *reactorSetup) {
		s.toolRegistry = reg
	}
}

// WithSkillRegistry sets a custom SkillRegistry implementation.
// Use this to provide embedding-based semantic skill matching, etc.
// If not set, DefaultSkillRegistry is used automatically.
func WithSkillRegistry(reg core.SkillRegistry) ReactorOption {
	return func(s *reactorSetup) {
		s.skillRegistry = reg
	}
}

// WithSessionStore sets a SessionStore for conversation persistence.
// The session store provides the backing store for the sliding window mechanism,
// enabling unlimited context through message persistence and token-budget-aware retrieval.
func WithSessionStore(store core.SessionStore) ReactorOption {
	return func(s *reactorSetup) {
		s.sessionStore = store
	}
}

// WithKVStore sets a KVStore for session-scoped key-value data sharing.
// Tools and skills can use KVStore to share small data (config, state, etc.)
// within the same session while being isolated from other sessions.
// If not set, a FileSystemKVStore is created automatically.
func WithKVStore(store core.KVStore) ReactorOption {
	return func(s *reactorSetup) {
		s.kvStore = store
	}
}

// WithFileStore sets a FileStore for session-scoped file storage.
// Tools and skills can use FileStore to read/write files within the same session
// while being isolated from other sessions. Useful for skill scripts and temp files.
// If not set, a FileSystemFileStore is created automatically.
func WithFileStore(store core.FileStore) ReactorOption {
	return func(s *reactorSetup) {
		s.fileStore = store
	}
}

// WithRuleRegistry sets a custom RuleRegistry for behavior rule management.
// Rules are injected into the System Prompt's ## Behavioral Rules section.
// There is no built-in default — external implementations must be provided.
func WithRuleRegistry(reg core.RuleRegistry) ReactorOption {
	return func(s *reactorSetup) {
		s.ruleRegistry = reg
	}
}


// WithProjectDir sets the project working directory for this Reactor (and its Agent).
// This is the primary way to ensure ToolContext.ProjectDir is always populated.
//
// Design-time safety guarantee:
//   - When set, all tools (edit/read/write) use this as their base directory
//   - LLM receives this in Environment section of system prompt via BuildEnvironmentInfoParams()
//   - Prevents runtime "file not found" errors from ambiguous working directory
//
// Usage:
//
//	reactor, err := reactor.NewReactor(cfg,
//	    reactor.WithProjectDir("/path/to/project"),
//	)
//
// Note: Agent layer's WithProjectDir() is typically used instead, which auto-injects here.
func WithProjectDir(dir string) ReactorOption {
	return func(s *reactorSetup) {
		s.projectDir = dir
	}
}

// WithSessionDir sets the session sandbox directory for this Reactor.
// When SessionStore is available, this is auto-resolved; otherwise set explicitly.
//
// When set:
//   - Tools can isolate session-specific files (temp files, drafts, etc.)
//   - LLM knows where to place session-scoped output
func WithSessionDir(dir string) ReactorOption {
	return func(s *reactorSetup) {
		s.sessionDir = dir
	}
}

// ── Hook 注入 Option ───────────────────────────────────────────────────────

// WithThoughtHooks 注入思考阶段 hooks。
// 用户 hooks 在 NewReactor 中追加到内置 hooks 后统一排序。
func WithThoughtHooks(hooks ...ThoughtHook) ReactorOption {
	return func(s *reactorSetup) {
		s.thoughtHooks = append(s.thoughtHooks, hooks...)
	}
}

// WithToolHooks 注入工具执行阶段 hooks。
func WithToolHooks(hooks ...ToolHook) ReactorOption {
	return func(s *reactorSetup) {
		s.toolHooks = append(s.toolHooks, hooks...)
	}
}

// WithObservationHooks 注入观察阶段 hooks。
func WithObservationHooks(hooks ...ObservationHook) ReactorOption {
	return func(s *reactorSetup) {
		s.observationHooks = append(s.observationHooks, hooks...)
	}
}

// WithStuckDetection 设置循环卡死检测器。
// detector 分析迭代历史，检测工具循环、错误循环、决策振荡、无进展等模式，
// 早期注入提示引导 LLM 自修正，三次仍无改进时硬终止。
//
// 使用 NewDefaultStuckDetector() 创建包含全部内置检测器的默认组合：
//
//	reactor.WithStuckDetection(reactor.NewDefaultStuckDetector())
//
// 也可自定义检测器组合：
//
//	reactor.WithStuckDetection(&reactor.CompositeDetector{
//	    Detectors: []reactor.StuckDetector{
//	        &reactor.ToolLoopDetector{Threshold: 3},
//	        &reactor.ErrorLoopDetector{Threshold: 2},
//	    },
//	})
//
// 如果未设置此选项，Reactor 循环不进行卡死检测。
func WithStuckDetection(detector StuckDetector) ReactorOption {
	return func(s *reactorSetup) {
		s.stuckAnalyzer = detector
	}
}
