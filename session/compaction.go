package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/DotNetAge/goharness/memory"
)

// ── Compaction helpers ────────────────────────────────────────────────────

// sessionState 是压缩决策用的会话状态快照。
//
// 偏移量模型：cursor 是当前窗口起始偏移量，
// activeMessages = messages[cursor:] 是即将被清空的活跃窗口。
type sessionState struct {
	cursor         int
	maxWindowSize  int64
	windowTokens   int64
	activeMessages []Message // 当前活跃窗口 messages[cursor:]
}

// captureState 在读锁下原子地捕获当前会话状态。
// 偏移量模型：windowTokens 基于 activeMessages（messages[cursor:]）计算。
func (s *Session) captureState() sessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	activeMessages := s.messages[s.cursor:]
	tokens := estimateWindowTokensV2(activeMessages)

	return sessionState{
		cursor:         s.cursor,
		maxWindowSize:  s.maxWindowSize,
		windowTokens:   tokens,
		activeMessages: activeMessages,
	}
}

// needsCompaction 判断当前状态是否需要触发摘要清空。
// 偏移量模型：只要活跃窗口非空且 WindowTokens 超过 80% 阈值即触发。
func (st sessionState) needsCompaction() bool {
	if st.maxWindowSize <= 0 {
		return false
	}
	if len(st.activeMessages) == 0 {
		return false
	}

	threshold := int64(float64(st.maxWindowSize) * 0.8)
	return st.windowTokens > threshold
}

// executeFullCompaction 在写锁内执行清空：cursor = len(messages)。
//
// 偏移量模型核心动作：
//   - 不删除 messages（完整历史保留在内存和持久化存储中）
//   - 只把 cursor 移到 messages 末尾，使当前窗口 messages[cursor:] 变为空切片
//   - 通过 SetCursor 持久化新的 cursor 偏移量
//   - 不调用 UpdateMessages —— 保留 session.yml 中的完整原始消息
//
// 返回被清空的活跃窗口消息数（len(messages) - 旧 cursor）。
func (s *Session) executeFullCompaction(ctx context.Context) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 被清空的活跃窗口消息数
	slidCount := len(s.messages) - s.cursor

	// 移动 cursor 到末尾 —— 当前窗口 messages[cursor:] 变为空切片
	s.cursor = len(s.messages)

	if s.store != nil {
		// 只持久化 cursor 偏移量，不删除 session.yml 中的原始消息
		_ = s.store.SetCursor(ctx, s.id, s.cursor)
	}

	if s.compactionHandler != nil {
		s.compactionHandler(CompactionEvent{
			MessagesSlid:   slidCount,
			RemainingAfter: 0,
			WindowSize:     s.maxWindowSize,
		})
	}

	return slidCount
}

// sanitizeMessagesForLLM 全局清洗消息序列，确保符合 Anthropic API 的角色交替规则：
//   - 移除末尾未配对的 tool_call/tool_result
//   - 移除序列中任何 tool_call 但后续没有足够的 tool 消息跟随的 assistant 消息中的 tool_calls
//   - 移除没有对应 pending tool_call 的孤立 tool 消息
func sanitizeMessagesForLLM(msgs []Message) []Message {
	if len(msgs) == 0 {
		return msgs
	}

	// 第一遍：构建 tool_call 期望表
	type expectedCall struct {
		required int // 需要的 tool 消息数
		got      int // 实际收到的 tool 消息数
	}
	exp := make(map[string]*expectedCall) // callID → expected
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role == "tool" {
			continue
		}
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			for _, tc := range m.ToolCalls {
				exp[tc.ID] = &expectedCall{required: 1, got: 0}
			}
		}
	}
	// 统计每个 call_id 实际有多少 tool 消息
	for _, m := range msgs {
		if m.Role == "tool" {
			if e, ok := exp[m.ToolCallID]; ok {
				e.got++
			}
		}
	}

	// 找出不完整的 tool_calls（期待 > 实际）
	incomplete := make(map[string]bool)
	for id, e := range exp {
		if e.got < e.required {
			incomplete[id] = true
		}
	}

	// 第二遍：构建干净的输出
	out := make([]Message, 0, len(msgs))
	pendingIncomplete := false // 当前 assistant 是否有不完整的 tool_call
	for _, m := range msgs {
		if m.Role == "assistant" && len(m.ToolCalls) > 0 {
			// 检查这个 assistant 的 tool_calls 是否全部完整
			allComplete := true
			for _, tc := range m.ToolCalls {
				if incomplete[tc.ID] {
					allComplete = false
					break
				}
			}
			if allComplete {
				out = append(out, m)
				pendingIncomplete = false
			} else {
				// 这个 assistant 的 tool_calls 不完整 → 只保留文字内容，去掉 ToolCalls
				cleaned := m
				cleaned.ToolCalls = nil
				out = append(out, cleaned)
				pendingIncomplete = true
			}
		} else if m.Role == "tool" {
			if incomplete[m.ToolCallID] || pendingIncomplete {
				continue // 跳过孤立的 tool 消息
			}
			out = append(out, m)
		} else {
			out = append(out, m)
			pendingIncomplete = false
		}
	}

	return out
}

// generateSummary 在任何锁之外调用摘要器以防止死锁。
// 把所有待摘要消息完整交给 LLM，由 LLM 借助 prompt 的 Quality rules
// 自主判断哪些值得记忆（识别 trivial/重复/纠正/冲突）。代码层不做源头过滤，
// 因为砍掉消息会让 LLM 失去完整上下文，反而降低摘要质量。
// 但必须先剔除末尾未配对的 tool_call/tool_result（Anthropic API 校验）。
func (s *Session) generateSummary(ctx context.Context, messages []Message) ([]memory.MemoryChunk, error) {
	if s.summarizer == nil || len(messages) == 0 {
		return nil, nil
	}

	// 剔除末尾未配对的 tool_call/tool_result，避免 LLM API 校验拒绝
	messages = sanitizeMessagesForLLM(messages)
	if len(messages) == 0 {
		s.logInfo("generateSummary: all messages stripped by LLM sanitization", "session_id", s.id)
		return nil, nil
	}

	chunks, err := s.summarizer.Summarize(ctx, messages)
	if err != nil {
		s.logError("generateSummary: Summarize failed", err, "session_id", s.id)
		return nil, err
	}
	if len(chunks) == 0 {
		s.logInfo("generateSummary: Summarize returned empty chunks (no substantive content)", "session_id", s.id)
		return nil, nil
	}

	// 用智能体名称、会话 ID、项目目录和时间戳回退值丰富 chunks。
	// 时间戳优先使用 LLM 在 JSON 中提供的事件时间；LLM 未填或解析失败时
	// 才回退到 summarize 触发时间。
	for i := range chunks {
		chunks[i].AgentName = s.agentName
		chunks[i].SessionID = s.id
		chunks[i].ProjectDir = s.projectDir
		if chunks[i].Timestamp.IsZero() {
			chunks[i].Timestamp = time.Now()
		}
		if chunks[i].ID == "" && chunks[i].Content != "" {
			h := sha256.Sum256([]byte(chunks[i].Content))
			chunks[i].ID = hex.EncodeToString(h[:])
		}
	}

	return chunks, nil
}

// persistSummary 将生成的记忆 chunks 存储到记忆存储中。
// 如果存储失败则返回错误；调用者必须将其视为压缩失败。
// 当 mem 为 nil（未配置记忆存储）时，这被视为错误 —
// 摘要预期会被持久化，而不是被静默丢弃。
func (s *Session) persistSummary(ctx context.Context, chunks []memory.MemoryChunk) error {
	if len(chunks) == 0 {
		return nil
	}
	if s.mem == nil {
		err := fmt.Errorf("persistSummary: memory store is nil, cannot persist summary chunks")
		s.logError("persistSummary: memory store not configured", err, "session_id", s.id)
		return err
	}

	if err := s.mem.StoreChunks(ctx, s.id, chunks); err != nil {
		s.logError("persistSummary: StoreChunks failed", err, "session_id", s.id)
		return err
	}
	return nil
}

// ── 压缩方法 ────────────────────────────────────────────────────────────

// doCompact 执行压缩的核心逻辑：摘要 + cursor 移动。
// 被 TryCompact 和 ForceCompact 共享，避免重复代码。
//
// 参数：
//   - ctx: 上下文，用于控制超时和取消
//   - state: 当前会话状态快照
//   - operation: 操作名称（"TryCompact" 或 "ForceCompact"），用于日志
func (s *Session) doCompact(ctx context.Context, state sessionState, operation string) {
	// 触发 compact start handler
	if s.compactStartHandler != nil {
		s.compactStartHandler(state.windowTokens, s.maxWindowSize)
	}

	// 摘要当前活跃窗口（无锁，允许 LLM I/O 阻塞）
	slidCount := 0
	summaryFailed := false
	if s.summarizer != nil && len(state.activeMessages) > 0 {
		s.logInfo(operation+": generating summary", "session_id", s.id,
			"active_messages", len(state.activeMessages))
		chunks, err := s.generateSummary(ctx, state.activeMessages)
		if err != nil {
			s.logError(operation+": summary generation FAILED, will not compact", err, "session_id", s.id)
			summaryFailed = true
		} else if len(chunks) == 0 {
			// generateSummary 返回 nil,nil 时（LLM 判定无实质信息 / sanitize 全剥离），
			// 不做 cursor 移动以免丢消息历史但不留记忆。
			s.logInfo(operation+": summary empty (no substantive content), skipped compaction", "session_id", s.id)
			summaryFailed = true
		} else {
			s.logInfo(operation+": summary generated", "session_id", s.id, "chunks", len(chunks))
			if err := s.persistSummary(ctx, chunks); err != nil {
				s.logError(operation+": persist summary FAILED, will not compact", err, "session_id", s.id)
				summaryFailed = true
			} else {
				s.logInfo(operation+": summary persisted", "session_id", s.id)
			}
		}
	} else if s.summarizer == nil {
		s.logInfo(operation+": no summarizer configured, skipping summary generation", "session_id", s.id)
	}

	if !summaryFailed {
		// 移动 cursor 到末尾（清空当前窗口，不删除历史消息）
		slidCount = s.executeFullCompaction(ctx)
		s.logInfo(operation+": cursor moved", "session_id", s.id, "slid_count", slidCount)
	} else {
		s.logInfo(operation+": skipped cursor movement due to summarization failure", "session_id", s.id)
	}

	// 触发 compact done handler
	afterTokens := s.CurrentWindowTokens()
	if s.compactDoneHandler != nil {
		s.compactDoneHandler(slidCount, afterTokens)
	}

	s.logInfo(operation+": done", "session_id", s.id, "after_tokens", afterTokens)
}

// TryCompact 检查当前会话窗口的 Token 是否超限，若超限则对活跃窗口
// (messages[cursor:]) 进行一次摘要、持久化到 MemoryStore(不是MemorySessionStore)，然后将 cursor
// 移到 messages 末尾（cursor = len(messages)），使当前窗口变为空切片。
//
// Cursor 语义（偏移量模型）：
//   - messages = 完整历史（不删除）
//   - cursor = 当前窗口起始偏移量
//   - 当前窗口 = messages[cursor:]
//   - 清空 = cursor = len(messages)（切片为空，但不删除历史消息）
//
// 触发条件：WindowTokens > 80% * maxWindowSize
// 调用时机：由 runtime 在新一个轮次开始前调用（不在 Append 末尾调用，
// 避免工具结果 append 中途触发清空破坏 tool_call 配对）
//
// 无锁设计：先 captureState（读锁快照）→ 无锁调用 summarizer（I/O）→
// 写锁内执行 cursor 移动。避免持锁期间进行 LLM 调用。
func (s *Session) TryCompact(ctx context.Context) {
	state := s.captureState()

	s.logInfo("TryCompact: entered", "session_id", s.id,
		"window_tokens", state.windowTokens,
		"max_window_size", state.maxWindowSize,
		"active_messages", len(state.activeMessages))

	if !state.needsCompaction() {
		if state.maxWindowSize <= 0 {
			s.logInfo("TryCompact: skipped (maxWindowSize=0, compaction disabled)", "session_id", s.id)
		} else if len(state.activeMessages) == 0 {
			s.logInfo("TryCompact: skipped (active window is empty)", "session_id", s.id)
		} else {
			threshold := int64(float64(state.maxWindowSize) * 0.8)
			s.logInfo("TryCompact: skipped (windowTokens <= threshold)", "session_id", s.id,
				"window_tokens", state.windowTokens,
				"threshold", threshold)
		}
		return
	}

	s.logInfo("TryCompact: triggered (windowTokens > 80% of maxWindowSize)", "session_id", s.id,
		"window_tokens", state.windowTokens,
		"max_window_size", state.maxWindowSize)

	s.doCompact(ctx, state, "TryCompact")
}

// ForceCompact 执行与 TryCompact 相同的压缩逻辑（摘要 + cursor 移动），
// 但使用独立阈值：仅当当前活跃窗口 tokens 超过 100K 时执行。
//
// 适用于前端手动触发的强制压缩（前端已通过 100K tokens 判断按钮可用性），
// 不应由 Runtime 自动调用。
func (s *Session) ForceCompact(ctx context.Context) {
	state := s.captureState()

	s.logInfo("ForceCompact: entered", "session_id", s.id,
		"window_tokens", state.windowTokens,
		"active_messages", len(state.activeMessages))

	if len(state.activeMessages) == 0 {
		s.logInfo("ForceCompact: skipped (active window is empty)", "session_id", s.id)
		return
	}

	const forceCompactThreshold int64 = 100_000
	if state.windowTokens <= forceCompactThreshold {
		s.logInfo("ForceCompact: skipped (windowTokens <= threshold)", "session_id", s.id,
			"window_tokens", state.windowTokens,
			"threshold", forceCompactThreshold)
		return
	}

	s.logInfo("ForceCompact: triggered (windowTokens > threshold)", "session_id", s.id,
		"window_tokens", state.windowTokens,
		"threshold", forceCompactThreshold)

	s.doCompact(ctx, state, "ForceCompact")
}

// SetCompactionHandler 更新在压缩事件后调用的回调函数。
// 可以随时调用以更改或移除处理程序。
//
// 传递 nil 以禁用压缩通知。
func (s *Session) SetCompactionHandler(h func(CompactionEvent)) {
	s.compactionHandler = h
}

// SetSummarizer 设置用于上下文压缩的基于 LLM 的摘要器。
// 可以随时调用以更改或移除摘要器。
// 传递 nil 以在压缩期间禁用摘要。
func (s *Session) SetSummarizer(ss Summarizer) {
	s.summarizer = ss
}

// SetMemory 设置用于持久化压缩摘要的记忆存储。
// 可以随时调用以更改或移除记忆存储。
// 传递 nil 以禁用摘要持久化。
func (s *Session) SetMemory(mem MemoryStore) {
	s.mem = mem
}

// SetMicroCompactDoneHandler 设置在 TryMicroCompact 完成后调用的回调。
// 回调接收 (compressed, deduped, windowTokens) 计数器。
// 传递 nil 以禁用。
func (s *Session) SetMicroCompactDoneHandler(h func(compressed, deduped int, windowTokens int64)) {
	s.microCompactDoneHandler = h
}

// SetCompactStartHandler 设置在 TryCompact 开始基于 LLM 的摘要压缩前调用的回调。
// 回调接收 (windowTokens, maxWindowSize)。
// 传递 nil 以禁用。
func (s *Session) SetCompactStartHandler(h func(windowTokens, maxWindowSize int64)) {
	s.compactStartHandler = h
}

// SetCompactDoneHandler 设置在 TryCompact 完成后调用的回调。
// 回调接收 (messagesSlid, windowTokens)。
// 传递 nil 以禁用。
func (s *Session) SetCompactDoneHandler(h func(messagesSlid int, windowTokens int64)) {
	s.compactDoneHandler = h
}

// SetMicroCompactStartHandler 设置在 TryMicroCompact 开始工具消息压缩前调用的回调。
// 回调接收 (windowTokens, maxWindowSize)。
// 传递 nil 以禁用。
func (s *Session) SetMicroCompactStartHandler(h func(windowTokens, maxWindowSize int64)) {
	s.microCompactStartHandler = h
}
