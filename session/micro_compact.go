package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// processContent 计算内容的 SHA256（前 32 个十六进制字符，128 位）和基于 DeepSeek 的 token 估算。
//
// Token 估算使用 DeepSeek 官方公式：
//   - ASCII/英文字符 ≈ 每个 0.3 token
//   - CJK/全角字符 ≈ 每个 0.6 token
func processContent(content string) (sha32 string, tokens int64) {
	h := sha256.New()
	var t float64
	for _, r := range content {
		h.Write([]byte(string(r)))
		if r <= 0x7F {
			t += 0.3
		} else {
			t += 0.6
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:32], int64(t) + 1
}

// estimateWindowTokensV2 估算给定消息在活跃上下文窗口中的 token 数。
//
// 对于有 Usage 数据的助手消息，只计算 CompletionTokens（+ ReasoningTokens）。
// 不能在这里使用 TotalTokens，因为它包含了随该请求发送的整个提示历史，
// 会重复计算窗口中已存在的早期消息。
//
// 对于已压缩消息（Compacted != ""），使用占位符大小（约 20 tokens）。
// 对于所有其他消息，回退到 DeepSeek 字符级估算。
func estimateWindowTokensV2(msgs []Message) int64 {
	var total int64
	for _, m := range msgs {
		if m.Compacted != "" {
			total += 20 // 占位符 "…" ≈ 20 tokens
		} else if m.Role == "assistant" && m.Usage != nil && (m.Usage.CompletionTokens > 0 || m.Usage.ReasoningTokens > 0) {
			total += int64(m.Usage.CompletionTokens + m.Usage.ReasoningTokens)
		} else {
			_, tokens := processContent(m.Content)
			total += tokens
		}
	}
	return total
}

// BuildToolNameByID 通过扫描助手消息的 ToolCalls 列表，构建从 tool_call_id → 工具名称的映射。
func BuildToolNameByID(msgs []Message) map[string]string {
	m := make(map[string]string)
	for _, msg := range msgs {
		if msg.Role != "assistant" {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" && tc.Name != "" {
				m[tc.ID] = tc.Name
			}
		}
	}
	return m
}

// RenderCompactedPlaceholder 返回替换 LLM 上下文中压缩工具消息内容的单行占位符。
//
// 格式：[已压缩] 工具: {tool} | {n} tokens | 路径: {path}
func RenderCompactedPlaceholder(msg Message, toolNameByID map[string]string) string {
	var meta CompactedMeta
	if err := json.Unmarshal([]byte(msg.Compacted), &meta); err != nil {
		// 压缩数据损坏 → 显示原始值作为后备
		return msg.Compacted
	}

	toolName := meta.ToolName
	if name, ok := toolNameByID[msg.ToolCallID]; ok && name != "" {
		toolName = name
	}

	return fmt.Sprintf("[已压缩] 工具: %s | %d tokens | 路径: %s",
		toolName, meta.TokenCount, meta.Path)
}

// stripDuplicateToolMessages 移除内容相同的相邻工具消息。
// 仅操作 role="tool" 消息，以避免破坏助手-工具配对。
// 返回去重后的切片和被移除的孤立 ToolCallID 集合。
func stripDuplicateToolMessages(msgs []Message) ([]Message, map[string]bool) {
	orphaned := make(map[string]bool)
	if len(msgs) < 2 {
		return msgs, orphaned
	}
	out := make([]Message, 0, len(msgs))
	out = append(out, msgs[0])
	for i := 1; i < len(msgs); i++ {
		prev := out[len(out)-1]
		curr := msgs[i]
		if curr.Role == "tool" && prev.Role == "tool" && curr.Content == prev.Content {
			// 将被跳过消息的 ToolCallID 标记为孤立，以便后续清理
			// 对应的 assistant ToolCall 条目。
			if curr.ToolCallID != "" {
				orphaned[curr.ToolCallID] = true
			}
			continue
		}
		out = append(out, curr)
	}
	return out, orphaned
}

// ── Session 方法：TryMicroCompact ────────────────────────────────────────

const (
	microCompactTriggerRatio  = 0.45 // 当窗口 >= 45% maxWindowSize 时开始压缩
	microCompactTargetRatio   = 0.40 // 当窗口 <= 40% maxWindowSize 时停止压缩
	microCompactPositionStart = 0.25 // 仅压缩 [25%, 65%] 位置范围内的消息
	microCompactPositionEnd   = 0.65
	microCompactMinTokens     = 500 // 跳过短消息（不值得压缩）
)

// TryMicroCompact 检查会话的活跃窗口是否超过 MicroCompact 触发阈值（maxWindowSize 的 45%）。
// 如果是，它压缩 25%-65% 位置范围内符合条件的工具消息，直到窗口降至 40% 以下，然后持久化会话。
//
// 如果执行了压缩并保存了会话，返回 true。
// 使用 s.SessionDir() 获取会话目录，无需外部传入。
func (s *Session) TryMicroCompact() bool {
	if s.ModelContextLength() <= 0 {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	window := s.messages[s.cursor:] // 偏移量模型：当前窗口 = messages[cursor:]
	if len(window) == 0 {
		return false
	}

	// Step 1：检查触发阈值
	windowTokens := estimateWindowTokensV2(window)
	triggerTokens := int64(float64(s.ModelContextLength()) * microCompactTriggerRatio)
	if windowTokens < triggerTokens {
		return false
	}

	// 触发 micro-compact 开始回调
	if s.microCompactStartHandler != nil {
		s.microCompactStartHandler(windowTokens, s.ModelContextLength())
	}

	// Step 2：先去除重复的工具消息（成本低，可减少噪声）
	deduped, orphanedIDs := stripDuplicateToolMessages(window)
	dedupCount := len(window) - len(deduped)
	hadDedup := dedupCount > 0
	if hadDedup {
		// 偏移量模型：保留历史分区 messages[:cursor]，用去重结果替换活跃窗口
		newMessages := make([]Message, 0, s.cursor+len(deduped))
		newMessages = append(newMessages, s.messages[:s.cursor]...)
		newMessages = append(newMessages, deduped...)
		s.messages = newMessages
		// cursor 不变 —— 仍指向历史分区边界
		// 清理被移除的重复工具消息对应的孤立 ToolCalls
		if len(orphanedIDs) > 0 {
			s.removeOrphanedToolCalls(s.cursor, orphanedIDs)
		}
	}
	// 从 s.messages 刷新 window，使候选项指针引用
	// 规范切片（在去重替换消息列表后尤为重要）。
	window = s.messages[s.cursor:]

	// Step 3：去重后重新计算
	windowTokens = estimateWindowTokensV2(window)
	if windowTokens < triggerTokens {
		return hadDedup
	}

	// Step 4：构建工具名称查找表
	toolNameByID := BuildToolNameByID(window)

	// Step 5：查找符合条件的候选项 —— role="tool"，位于 25%-65% 位置，
	// content > 500 tokens，compacted 为空
	type candidate struct {
		idx       int
		msg       *Message
		timestamp int64
	}
	positionStart := int(float64(len(window)) * microCompactPositionStart)
	positionEnd := int(float64(len(window)) * microCompactPositionEnd)
	targetTokens := int64(float64(s.ModelContextLength()) * microCompactTargetRatio)

	var candidates []candidate
	for i := positionStart; i < positionEnd && i < len(window); i++ {
		m := &window[i]
		if m.Role != "tool" || m.Compacted != "" {
			continue
		}
		_, tokens := processContent(m.Content)
		if tokens <= microCompactMinTokens {
			continue
		}
		candidates = append(candidates, candidate{idx: i, msg: m, timestamp: m.Timestamp})
	}

	if len(candidates) == 0 {
		return hadDedup
	}

	// 按时间戳升序排序（最旧的在前）
	sort.Slice(candidates, func(a, b int) bool {
		return candidates[a].timestamp < candidates[b].timestamp
	})

	// Step 6：逐个压缩候选项，直到低于目标阈值
	sessionDir := s.SessionDir()
	var compressed int
	for _, c := range candidates {
		if windowTokens <= targetTokens {
			break
		}

		sha32, tokenCount := processContent(c.msg.Content)

		// 写入缓存文件
		if sessionDir != "" {
			cacheDir := filepath.Join(sessionDir, "microcompact")
			if err := os.MkdirAll(cacheDir, 0755); err == nil {
				cachePath := filepath.Join(cacheDir, sha32+".md")
				if err := os.WriteFile(cachePath, []byte(c.msg.Content), 0644); err == nil {
					meta := CompactedMeta{
						Path:       cachePath,
						ToolName:   toolNameByID[c.msg.ToolCallID],
						TokenCount: tokenCount,
					}
					data, _ := json.Marshal(meta)
					c.msg.Compacted = string(data)
					// 压缩后的消息现在仅占约 20 tokens（占位符），
					// 而非 tokenCount。减去二者差值。
					windowTokens -= (tokenCount - 20)
					compressed++
				}
			}
		}
	}

	// Step 7：将压缩后的变更和 cursor 持久化到 store
	if compressed > 0 || hadDedup {
		if s.store != nil {
			if err := s.store.UpdateMessages(context.Background(), s.id, s.cursor, s.messages); err != nil {
				// 仅记录日志但不视为失败 —— 内存中的压缩状态仍然正确
			}
		}
		if s.microCompactDoneHandler != nil {
			s.microCompactDoneHandler(compressed, dedupCount, windowTokens)
		}
		return true
	}

	return false
}

// removeOrphanedToolCalls 从助手消息中移除 tool_call_id 在孤立集合中的 ToolCall 条目。
// 这在去重后调用，以防止孤立 ToolCall 条目导致"工具消息不足"错误。
func (s *Session) removeOrphanedToolCalls(cursor int, orphanedIDs map[string]bool) {
	for i := cursor; i < len(s.messages); i++ {
		m := &s.messages[i]
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		kept := make([]ToolCall, 0, len(m.ToolCalls))
		for _, tc := range m.ToolCalls {
			if !orphanedIDs[tc.ID] {
				kept = append(kept, tc)
			}
		}
		if len(kept) != len(m.ToolCalls) {
			m.ToolCalls = kept
		}
	}
}


