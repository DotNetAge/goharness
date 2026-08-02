package loop

import (
	"strings"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/memory"
)

// MemoryThoughtHook 在每次 LLM 调用前，按时间倒序取最新 10 条记忆
// （当前 AgentName+ProjectDir 范围），拼到系统指令区末尾（单 system message）。
//
// 新语义（memmache.md）：
//   - 不再用语义检索，改为按时间倒序固定取前 10 条
//   - 范围：当前 AgentName + ProjectDir（跨会话）
//   - 注入位置：系统指令区末尾（单 system message，KV 缓存友好）
//   - 记忆条目按时间顺序由旧至新插入
type MemoryThoughtHook struct {
	memory memory.Memory
	Logger logging.Logger
}

// NewMemoryThoughtHook creates a new MemoryThoughtHook with the given memory store.
func NewMemoryThoughtHook(mem memory.Memory) *MemoryThoughtHook {
	return &MemoryThoughtHook{memory: mem}
}

// Priority returns the priority for MemoryThoughtHook (50).
func (h *MemoryThoughtHook) Priority() int { return 50 }

// BeforeLLM 按时间倒序取最新记忆并注入到系统指令区末尾。
//
// 通过类型断言检查 Memory 是否实现 LatestRetriever 可选接口；
// 未实现则跳过（保持兼容性）。
func (h *MemoryThoughtHook) BeforeLLM(sessionID string, iteration int, input *hooks.CallInput) hooks.HookResult {
	if h.memory == nil || input.AgentName == "" {
		h.Logger.Debug("MemoryThoughtHook: 跳过，记忆存储为空或 AgentName 为空",
			"session_id", sessionID, "memory_nil", h.memory == nil, "agent", input.AgentName)
		return hooks.HookResult{}
	}

	// 先检查 system prompt 是否已注入过记忆内容，避免重复查询和注入
	if len(input.SystemPromptSections) > 0 {
		last := &input.SystemPromptSections[len(input.SystemPromptSections)-1]
		for _, block := range last.Content {
			if strings.Contains(block.Text, "## 历史对话摘要") {
				h.Logger.Debug("MemoryThoughtHook: 已注入记忆，跳过",
					"session_id", sessionID, "agent", input.AgentName)
				return hooks.HookResult{}
			}
		}
	}

	// 通过类型断言检查是否支持 RetrieveLatest（时间倒序，不依赖向量检索）
	latestRetriever, ok := h.memory.(memory.LatestRetriever)
	if !ok {
		h.Logger.Debug("MemoryThoughtHook: 记忆存储不支持 LatestRetriever 接口",
			"session_id", sessionID, "agent", input.AgentName)
		return hooks.HookResult{}
	}

	const latestLimit = 20
	records, err := latestRetriever.RetrieveLatest(nil, input.AgentName, input.ProjectDir, latestLimit)
	if err != nil {
		h.Logger.Debug("MemoryThoughtHook: RetrieveLatest 失败",
			"session_id", sessionID, "agent", input.AgentName, "project", input.ProjectDir, "error", err)
		return hooks.HookResult{}
	}
	if len(records) == 0 && sessionID != "" {
		// Fallback: agent+project 过滤空结果时按 sessionID 捞回
		if sessionRetriever, ok := h.memory.(memory.SessionRetriever); ok {
			h.Logger.Debug("MemoryThoughtHook: 回退到 RetrieveBySession 接口",
				"session_id", sessionID, "agent", input.AgentName)
			records, err = sessionRetriever.RetrieveBySession(nil, sessionID, latestLimit)
			if err != nil {
				h.Logger.Debug("MemoryThoughtHook: RetrieveBySession 失败",
					"session_id", sessionID, "error", err)
				return hooks.HookResult{}
			}
			if len(records) > 0 {
				h.Logger.Info("MemoryThoughtHook: 从会话回退获取记忆记录",
					"session_id", sessionID, "count", len(records))
			}
		}
	}
	if len(records) == 0 {
		h.Logger.Debug("MemoryThoughtHook: 未找到记忆记录",
			"session_id", sessionID, "agent", input.AgentName, "project", input.ProjectDir)
		return hooks.HookResult{}
	}
	h.Logger.Info("MemoryThoughtHook: 获取到记忆记录",
		"session_id", sessionID, "agent", input.AgentName, "project", input.ProjectDir, "count", len(records))

	// memmache.md: "将记忆条目按时间顺序由旧至新插入至摘要缓冲区"
	// records 已是倒序（最新在前），反转为正序（旧至新）注入
	reversed := make([]memory.MemoryChunk, len(records))
	for i, r := range records {
		reversed[len(records)-1-i] = r
	}

	content := memory.FormatMemoryRecords(reversed)
	if content == "" {
		h.Logger.Debug("MemoryThoughtHook: 格式化后的记忆内容为空",
			"session_id", sessionID, "agent", input.AgentName)
		return hooks.HookResult{}
	}
	h.Logger.Debug("MemoryThoughtHook: 格式化后的记忆内容长度",
		"session_id", sessionID, "agent", input.AgentName, "content_len", len(content))

	// 拼到最后一条 system message 的内容末尾（不新增第二条 system message）
	if len(input.SystemPromptSections) > 0 {
		last := &input.SystemPromptSections[len(input.SystemPromptSections)-1]
		memText := "\n\n## 历史对话摘要\n\n以下为与用户过往对话的决策记录，作为延续上下文使用；\n\n" + content
		last.Content = append(last.Content, gochatcore.ContentBlock{Type: "text", Text: memText})
		h.Logger.Info("MemoryThoughtHook: 已将记忆注入系统指令",
			"session_id", sessionID, "agent", input.AgentName, "records", len(records), "mem_bytes", len(memText))
	}
	return hooks.HookResult{}
}

// AfterLLM is a no-op for MemoryThoughtHook.
func (h *MemoryThoughtHook) AfterLLM(_ string, _ int, _ *hooks.LLMResponse, _ []hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

// Abort is a no-op for MemoryThoughtHook.
func (h *MemoryThoughtHook) Abort(_ string, _ string) {}
