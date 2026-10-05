package session

import (
	"context"
	"fmt"
	"time"
)

// ── 压缩辅助函数 ────────────────────────────────────────────────────────

// estimateTokens 基于 DeepSeek 官方公式估算文本的 token 数：
//   - ASCII/英文字符 ≈ 每个 0.3 token
//   - CJK/全角字符 ≈ 每个 0.6 token
func estimateTokens(content string) int64 {
	var t float64
	for _, r := range content {
		if r <= 0x7F {
			t += 0.3
		} else {
			t += 0.6
		}
	}
	return int64(t) + 1
}

// estimateWindowTokens 估算给定消息序列的 token 数。
//
// 对于有 Usage 数据的助手消息，只计算 CompletionTokens（+ ReasoningTokens）。
// 不能在这里使用 TotalTokens，因为它包含了随该请求发送的整个提示历史，
// 会重复计算窗口中已存在的早期消息。
//
// 对于所有其他消息，回退到 DeepSeek 字符级估算。
func estimateWindowTokens(msgs []Message) int64 {
	var total int64
	for _, m := range msgs {
		if m.Role == "assistant" && m.Usage != nil && (m.Usage.CompletionTokens > 0 || m.Usage.ReasoningTokens > 0) {
			total += int64(m.Usage.CompletionTokens + m.Usage.ReasoningTokens)
		} else {
			total += estimateTokens(m.Content)
		}
	}
	return total
}

// compactionCooldown 是自动压缩（TryCompact）失败后的冷却时长。
// 冷却期内 TryCompact 直接跳过，避免 LLM 失败或空返回时每轮重试形成死循环
// （每次重试最多耗时 compactionTimeout=10min 并消耗 token）。ForceCompact 不受约束。
const compactionCooldown = 5 * time.Minute

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
	tokens := estimateWindowTokens(activeMessages)

	return sessionState{
		cursor:         s.cursor,
		maxWindowSize:  s.ModelContextLength(),
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
			WindowSize:     s.ModelContextLength(),
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

// generateSummary 在任何锁之外调用压缩器以防止死锁。
// 把所有待摘要消息完整交给 LLM，由 LLM 按压缩指令自主提炼摘要原文（markdown）。
// 代码层不做源头过滤，因为砍掉消息会让 LLM 失去完整上下文，反而降低摘要质量。
// 但必须先剔除末尾未配对的 tool_call/tool_result（Anthropic API 校验）。
func (s *Session) generateSummary(ctx context.Context, messages []Message) (string, error) {
	if s.compactor == nil || len(messages) == 0 {
		return "", nil
	}

	// 剔除末尾未配对的 tool_call/tool_result，避免 LLM API 校验拒绝。
	// 与 AssembleMessages 内部的 stripOrphanedToolCalls 幂等共存，不冲突。
	messages = sanitizeMessagesForLLM(messages)
	if len(messages) == 0 {
		s.logInfo("generateSummary: 未生成有效摘要，所有消息都被剔除后为空", "session_id", s.id)
		return "", nil
	}

	// 委托 compactor 执行 LLM 调用。compactor 由 agents 层注入，
	// 复用主对话的 system/tools/messages 前缀以命中 KV 缓存，
	// 并返回压缩摘要原文（markdown），由调用方经 SummarySink 持久化。
	return s.compactor.Compact(ctx, s, messages)
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
	// 未装配摘要交付回调时视为压缩失败：摘要无处持久化，清窗口等于丢历史不留记忆。
	// 记入冷却避免每轮重试白烧 token。ForceCompact（手动触发）不受冷却约束但同样记录。
	if s.summarySink == nil {
		s.logError(operation+": 未装配摘要交付回调（SummarySink），视为压缩失败",
			fmt.Errorf("summary sink 未装配"), "session_id", s.id)
		s.lastCompactionFailAt.Store(time.Now().UnixNano())
		return
	}

	// 触发 compact start handler
	if s.compactStartHandler != nil {
		s.compactStartHandler(state.windowTokens, s.ModelContextLength())
	}

	// 摘要当前活跃窗口（无锁，允许 LLM I/O 阻塞）
	slidCount := 0
	compactionFailed := false
	if s.compactor != nil && len(state.activeMessages) > 0 {
		s.logInfo(operation+": 开始生成摘要", "session_id", s.id,
			"active_messages", len(state.activeMessages))
		summary, err := s.generateSummary(ctx, state.activeMessages)
		if err != nil {
			s.logError(operation+": 生成摘要失败，将不压缩", err, "session_id", s.id)
			compactionFailed = true
		} else if summary == "" {
			// generateSummary 返回空串时（LLM 判定无实质信息 / sanitize 全剥离），
			// 不做 cursor 移动以免丢消息历史但不留记忆。
			s.logInfo(operation+": 生成摘要为空，跳过压缩", "session_id", s.id)
			compactionFailed = true
		} else {
			s.logInfo(operation+": 生成摘要成功，开始交付外部持久化", "session_id", s.id, "summary_bytes", len(summary))
			if err := s.summarySink(ctx, summary); err != nil {
				s.logError(operation+": 摘要交付失败（SummarySink），将不压缩", err, "session_id", s.id)
				compactionFailed = true
			} else {
				s.logInfo(operation+": 摘要交付成功", "session_id", s.id)
			}
		}
	} else if s.compactor == nil {
		// 未装配压缩器视为压缩失败：不允许 cursor 照常移动。
		// 否则会出现「清了窗口但零摘要落盘」的假成功。
		s.logError(operation+": 未配置压缩器，跳过摘要生成，视为压缩失败",
			fmt.Errorf("compactor 未装配"), "session_id", s.id)
		compactionFailed = true
	}

	if !compactionFailed {
		// 移动 cursor 到末尾（清空当前窗口，不删除历史消息）
		slidCount = s.executeFullCompaction(ctx)
		s.logInfo(operation+": 摘要持久化成功，游标移动到末尾", "session_id", s.id, "slid_count", slidCount)
	} else {
		s.logInfo(operation+": 摘要持久化失败，跳过游标移动", "session_id", s.id)
	}

	// 记录/清零压缩失败时间戳，驱动 TryCompact 冷却逻辑。
	// ForceCompact 不读冷却，但其失败同样会记录，避免手动失败后自动重试陷入同一问题。
	if compactionFailed {
		s.lastCompactionFailAt.Store(time.Now().UnixNano())
	} else {
		s.lastCompactionFailAt.Store(0)
	}

	// 触发 compact done handler
	afterTokens := s.CurrentWindowTokens()
	if s.compactDoneHandler != nil {
		s.compactDoneHandler(slidCount, afterTokens)
	}

	s.logInfo(operation+": done", "session_id", s.id, "after_tokens", afterTokens)
}

// TryCompact 检查当前会话窗口的 Token 是否超限，若超限则对活跃窗口
// (messages[cursor:]) 执行一次摘要（原文经 SummarySink 交付外部持久化），
// 然后将 cursor 移到 messages 末尾（cursor = len(messages)），使当前窗口变为空切片。
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
// 无锁设计：先 captureState（读锁快照）→ 无锁调用 compactor（I/O）→
// 写锁内执行 cursor 移动。避免持锁期间进行 LLM 调用。
func (s *Session) TryCompact(ctx context.Context) {
	// 冷却：上次压缩失败后短期内不重试，避免 LLM 失败/空返回时每轮触发死循环。
	// ForceCompact（手动触发）不受此约束。
	if failNano := s.lastCompactionFailAt.Load(); failNano > 0 {
		if elapsed := time.Since(time.Unix(0, failNano)); elapsed < compactionCooldown {
			s.logInfo("TryCompact: 压缩冷却中（上次失败），跳过",
				"session_id", s.id,
				"elapsed_since_fail", elapsed.String(),
				"cooldown", compactionCooldown.String())
			return
		}
	}

	state := s.captureState()

	s.logInfo("TryCompact: entered", "session_id", s.id,
		"window_tokens", state.windowTokens,
		"max_window_size", state.maxWindowSize,
		"active_messages", len(state.activeMessages))

	if !state.needsCompaction() {
		if state.maxWindowSize <= 0 {
			s.logInfo("TryCompact: 未配置最大窗口大小，跳过压缩", "session_id", s.id)
		} else if len(state.activeMessages) == 0 {
			s.logInfo("TryCompact: 活跃窗口为空，跳过压缩", "session_id", s.id)
		} else {
			threshold := int64(float64(state.maxWindowSize) * 0.8)
			s.logInfo("TryCompact: 当前窗口 tokens 未超过 80% * 最大窗口大小，跳过压缩", "session_id", s.id,
				"window_tokens", state.windowTokens,
				"threshold", threshold)
		}
		return
	}

	s.logInfo("TryCompact: 当前窗口 tokens 超过 80% * 最大窗口大小，触发压缩", "session_id", s.id,
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
		s.logInfo("ForceCompact: 活跃窗口为空，跳过压缩", "session_id", s.id)
		return
	}

	const forceCompactThreshold int64 = 100_000
	if state.windowTokens <= forceCompactThreshold {
		s.logInfo("ForceCompact: 当前窗口 tokens 未超过 100K，跳过压缩", "session_id", s.id,
			"window_tokens", state.windowTokens,
			"threshold", forceCompactThreshold)
		return
	}

	s.logInfo("ForceCompact: 当前窗口 tokens 超过 100K，触发压缩", "session_id", s.id,
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

// SetCompactor 设置用于上下文压缩的压缩器（依赖倒置，由 agents 层实现）。
// 可以随时调用以更改或移除压缩器。传递 nil 以禁用压缩摘要生成。
//
// Compactor 负责构造与主对话请求字段一致的 LLM 调用（system + tools + messages
// 前缀逐 token 一致，仅末尾追加压缩指令），以命中 KV 前缀缓存。
func (s *Session) SetCompactor(c Compactor) {
	s.compactor = c
}

// SetSummarySink 设置压缩摘要的交付回调（依赖倒置：摘要存哪、存成什么格式由应用层决定）。
// 在 cursor 移动前同步调用，返回 error 则本次压缩视为失败。
// 传递 nil 以禁用摘要交付（压缩将视为失败）。
func (s *Session) SetSummarySink(h func(ctx context.Context, summary string) error) {
	s.summarySink = h
}

// SetCompactionPrompt 覆盖压缩指令（空 = 使用压缩器内置默认）。
// 可以随时调用；下一次压缩即生效。
func (s *Session) SetCompactionPrompt(prompt string) {
	s.compactionPrompt = prompt
}

// CompactionPrompt 返回外部覆盖的压缩指令（空 = 未覆盖，压缩器使用内置默认）。
// 供 Compactor 实现构造压缩请求时读取。
func (s *Session) CompactionPrompt() string {
	return s.compactionPrompt
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
