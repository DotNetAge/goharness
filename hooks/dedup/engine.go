package dedup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/session"
)

// TryDedup is the shared deduplication core logic.
// It scans the session's active window for a previous tool call with the
// same parameters. On cache hit it synthesises a result (with a [复用: hash]
// marker), marks the old message's Compacted as [内容编号: hash], and returns
// the synthetic ToolResult. On miss it returns nil.
func TryDedup(sess *session.Session, params map[string]any, policy DedupPolicy) *hooks.ToolResult {
	currentHash := policy.ContentKey(params)
	window := sess.Current()

	// Step 1: build a lookup table: toolCallID → tool result info
	type toolResultInfo struct {
		content   string
		compacted string
	}
	toolResults := make(map[string]toolResultInfo, len(window))
	for _, m := range window {
		if m.Role == "tool" && m.ToolCallID != "" {
			toolResults[m.ToolCallID] = toolResultInfo{
				content:   m.Content,
				compacted: m.Compacted,
			}
		}
	}

	// Step 2: scan assistant messages for matching tool calls
	for i := len(window) - 1; i >= 0; i-- {
		m := window[i]
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		for _, tc := range m.ToolCalls {
			if tc.Name != policy.ToolName() {
				continue
			}

			// Parse Arguments (JSON string → map) and recompute hash on the fly.
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err != nil {
				continue
			}
			if policy.ContentKey(args) != currentHash {
				continue
			}

			// Found a matching tool call → look up its tool result.
			cached, ok := toolResults[tc.ID]
			if !ok {
				continue
			}

			// Skip if this message was already marked as a reference.
			if strings.HasPrefix(cached.compacted, "[内容编号: ") {
				continue
			}

			// ── Cache hit ───────────────────────────────────────────────

			content := cached.content
			if cached.compacted != "" {
				// Content was archived by MicroCompact, restore from cache file.
				restored, err := restoreCompactedContent(cached.compacted)
				if err != nil {
					continue // fail open – execute the tool normally
				}
				content = restored
			}

			// Strip any [复用: hash]\n prefix from a previously synthesised
			// result to avoid prefix accumulation on repeated dedup.
			if strings.HasPrefix(content, "[复用: ") {
				if idx := strings.Index(content, "\n"); idx >= 0 {
					content = content[idx+1:]
				}
			}

			// Mark the old message's Compacted with the reference tag.
			_ = sess.MarkAsContentRef(tc.ID, fmt.Sprintf("[内容编号: %s]", currentHash))

			// Synthesise the new tool result.
			synthesised := fmt.Sprintf("[复用: %s]\n%s", currentHash, content)

			return &hooks.ToolResult{
				ToolName: policy.ToolName(),
				Result:   synthesised,
				Success:  true,
			}
		}
	}

	return nil
}

// restoreCompactedContent reads the original content from a MicroCompact
// cache file.  Returns an error if the cache file is missing or unreadable.
func restoreCompactedContent(compactedJSON string) (string, error) {
	var meta session.CompactedMeta
	if err := json.Unmarshal([]byte(compactedJSON), &meta); err != nil {
		return "", err
	}
	if meta.Path == "" {
		return "", fmt.Errorf("empty cache path in compacted meta")
	}
	data, err := os.ReadFile(meta.Path)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// NormalizeContentKey is the default parameter normaliser for ContentKey.
// It sorts parameter keys to produce a stable hash regardless of JSON key order.
func NormalizeContentKey(toolName string, params map[string]any) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	h.Write([]byte(toolName))
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte("="))
		//nolint:errcheck // json.Marshal on map values never fails for supported types
		v, _ := json.Marshal(params[k])
		h.Write(v)
		h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))[:10]
}
