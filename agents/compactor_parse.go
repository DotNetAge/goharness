package agents

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/memory"
)

// ── 压缩响应解析（compactor 内部步骤）─────────────────────────────
//
// 这组函数是 compactor 的内部解析步骤，不发起 LLM 调用、无 I/O 副作用。
// 它们把 LLM 返回的 JSON 文本解析为 []memory.MemoryChunk，仅此而已。
//
// 不导出：解析是 Compactor 的实现细节，不是 session 或外部应用的扩展点。

// parseCompactionResponse 解析 LLM 压缩返回的 JSON 文本为 MemoryChunk 列表。
// 空字符串 / 空对象 / 空数组返回 nil,nil（LLM 判定无实质信息）。
func parseCompactionResponse(response string) ([]memory.MemoryChunk, error) {
	text := preprocessCompactionJSON(response)
	if text == "" {
		return nil, nil
	}

	rawChunks, err := unmarshalCompactionChunks(text)
	if err != nil {
		// 记录原始输出前 200 字符便于调试
		preview := text
		if len(preview) > 200 {
			preview = text[:200]
		}
		return nil, fmt.Errorf("compactor: LLM 输出不是有效的 JSON 数组 (preview: %q), discarded: %w", preview, err)
	}
	return buildCompactionChunks(rawChunks), nil
}

// preprocessCompactionJSON 清理和预处理 LLM 原始输出，提取 JSON 文本。
// 返回空字符串表示无实质信息。
func preprocessCompactionJSON(response string) string {
	text := strings.TrimSpace(response)

	// 尝试提取 JSON（可能被 markdown 代码块包裹）
	if strings.HasPrefix(text, "```") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimPrefix(text, "```")
		if idx := strings.LastIndex(text, "```"); idx >= 0 {
			text = strings.TrimSpace(text[:idx])
		}
	}
	text = strings.TrimSpace(text)

	// 空响应、空对象、空数组 — LLM 判定无实质信息
	if text == "" || text == "{}" || text == "[]" {
		return ""
	}
	return text
}

// unmarshalCompactionChunks 尝试将文本解析为 rawCompactionChunk 数组。
// 首次失败时会对非法转义序列做清洗后重试。
func unmarshalCompactionChunks(text string) ([]rawCompactionChunk, error) {
	var rawChunks []rawCompactionChunk
	if err := json.Unmarshal([]byte(text), &rawChunks); err != nil {
		// 清洗非法转义序列后重试（如 \   → 空格，\x → x）
		sanitized := sanitizeCompactionJSON(text)
		if sanitized != text {
			var retry []rawCompactionChunk
			if err2 := json.Unmarshal([]byte(sanitized), &retry); err2 == nil {
				return retry, nil
			}
		}
		return nil, err
	}
	return rawChunks, nil
}

// buildCompactionChunks 将 rawCompactionChunk 列表转为 MemoryChunk 列表，过滤空 content。
func buildCompactionChunks(rawChunks []rawCompactionChunk) []memory.MemoryChunk {
	if len(rawChunks) == 0 {
		return nil
	}
	chunks := make([]memory.MemoryChunk, 0, len(rawChunks))
	for _, rc := range rawChunks {
		if rc.Content == "" {
			continue
		}
		chunks = append(chunks, buildCompactionChunkFromRaw(rc))
	}
	if len(chunks) == 0 {
		return nil
	}
	return chunks
}

// invalidCompactionJSONEscapeRe 匹配 JSON 字符串中反斜杠后跟非法转义字符的序列。
// 合法 JSON 转义：\" \\ \/ \b \f \n \r \t \u
var invalidCompactionJSONEscapeRe = regexp.MustCompile(`\\([^"\\/bfnrtu])`)

// sanitizeCompactionJSON 尝试修复 LLM 输出的常见 JSON 格式问题：
//   - 移除非法转义序列（如 \后跟空格 → 仅保留空格）
//
// 只对明显非法的序列做替换，不会改变合法 JSON。
func sanitizeCompactionJSON(text string) string {
	return invalidCompactionJSONEscapeRe.ReplaceAllString(text, "$1")
}

// rawCompactionChunk 是解析 LLM 压缩输出 JSON 时使用的中间结构体，采用三段式。
// Title/Summary/Content 三段各司其职：Title 是导航标题、Summary 是核心结论、Content 是分条细节。
// Timestamp 是字符串（LLM 输出 ISO 8601），解析失败保持零值，由上层 fallback。
type rawCompactionChunk struct {
	Title     string   `json:"title,omitempty"`
	Summary   string   `json:"summary"`
	Content   string   `json:"content"`
	Tags      []string `json:"tags"`
	Timestamp string   `json:"timestamp,omitempty"`
}

// buildCompactionChunkFromRaw 将 rawCompactionChunk 转为 MemoryChunk，映射三段式 Title/Summary/Content。
// Timestamp 解析 LLM 提供的 ISO 8601 字符串；解析失败保持零值，由上层 fallback。
func buildCompactionChunkFromRaw(rc rawCompactionChunk) memory.MemoryChunk {
	tags := rc.Tags
	if tags == nil {
		tags = []string{}
	}
	chunk := memory.MemoryChunk{
		Title:   rc.Title,
		Summary: rc.Summary,
		Content: rc.Content,
		Tags:    tags,
	}
	if rc.Timestamp != "" {
		if t, err := time.Parse(time.RFC3339, rc.Timestamp); err == nil {
			chunk.Timestamp = t
		}
	}
	return chunk
}
