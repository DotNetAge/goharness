package reactor

import (
	"fmt"
	"sort"

	"github.com/DotNetAge/goreact/core"
)

// ToolResultBudgetEnforcer applies two-phase budget control to tool results:
//
// Phase 1 (Per-Tool): If a result exceeds the tool's MaxResultSizeChars,
// persist it to disk and replace with a <persisted-output> preview tag.
//
// Phase 2 (Per-Action): If the aggregate of all results still exceeds
// MaxToolResultsPerMessageChars, suppress the largest Phase-1-replaced
// results with compact suppression markers.
type ToolResultBudgetEnforcer struct {
	persister  *core.DiskResultPersister
	limits     core.ToolResultLimits
	state      *core.ContentReplacementState
	toolLookup func(name string) *core.ToolInfo
}

func NewToolResultBudgetEnforcer(
	persister *core.DiskResultPersister,
	limits core.ToolResultLimits,
	state *core.ContentReplacementState,
	toolLookup func(name string) *core.ToolInfo,
) *ToolResultBudgetEnforcer {
	return &ToolResultBudgetEnforcer{
		persister:  persister,
		limits:     limits,
		state:      state,
		toolLookup: toolLookup,
	}
}

// Enforce applies Phase 1 + Phase 2 budget control to a batch of tool results.
// Results are modified in place and the same slice is returned.
func (e *ToolResultBudgetEnforcer) Enforce(results []ToolResult) []ToolResult {
	if len(results) == 0 || e.persister == nil {
		return results
	}

	// Phase 1: Per-tool threshold check
	type phase1Info struct {
		originalSize int
		wasReplaced  bool
	}
	info := make([]phase1Info, len(results))

	for i, tr := range results {
		// Derive a unique state key. Empty ToolCallID falls back
		// to toolName_index (legacy path) to avoid key collisions.
		stateKey := tr.ToolCallID
		if stateKey == "" {
			stateKey = fmt.Sprintf("%s_%d", tr.ToolName, i)
		}

		// Skip if already processed in a previous cycle (frozen)
		if !e.state.IsFresh(stateKey) {
			if replacement, ok := e.state.GetReplacement(stateKey); ok {
				results[i].Result = replacement
				info[i].wasReplaced = true
			}
			info[i].originalSize = len([]rune(tr.Result))
			continue
		}

		// Determine threshold for this tool
		threshold := e.limits.MaxResultSizeChars
		skipPersist := false
		if toolInfo := e.toolLookup(tr.ToolName); toolInfo != nil {
			if toolInfo.MaxResultSizeChars == -1 {
				skipPersist = true
			} else if toolInfo.MaxResultSizeChars > 0 {
				threshold = toolInfo.MaxResultSizeChars
			}
		}

		if skipPersist {
			e.state.MarkExecuted(stateKey)
			info[i].originalSize = len([]rune(tr.Result))
			continue
		}

		runeCount := len([]rune(tr.Result))
		info[i].originalSize = runeCount

		if runeCount > threshold {
			// Persist and replace with preview tag
			tag, ok := e.persister.PersistWithTag(tr.ToolName, tr.ToolCallID, tr.Result)
			if ok {
				results[i].Result = tag
				e.state.MarkReplaced(stateKey, tag)
				info[i].wasReplaced = true
				continue
			}
		}

		e.state.MarkExecuted(stateKey)
	}

	// Phase 2: Per-action aggregate budget check
	totalSize := 0
	for _, tr := range results {
		totalSize += len(tr.Result)
	}

	if totalSize > e.limits.MaxToolResultsPerMessageChars {
		// Collect Phase-1-replaced results as suppressible candidates
		type suppressCandidate struct {
			index        int
			originalSize int
			toolName     string
		}
		var candidates []suppressCandidate
		for i, pi := range info {
			if pi.wasReplaced {
				candidates = append(candidates, suppressCandidate{
					index:        i,
					originalSize: pi.originalSize,
					toolName:     results[i].ToolName,
				})
			}
		}

		// Sort by original size descending (largest output suppressed first)
		sort.Slice(candidates, func(i, j int) bool {
			return candidates[i].originalSize > candidates[j].originalSize
		})

		remaining := totalSize
		for _, c := range candidates {
			if remaining <= e.limits.MaxToolResultsPerMessageChars {
				break
			}
			currentSize := len(results[c.index].Result)
			suppressMsg := fmt.Sprintf(
				"[Result #%d suppressed: %d KB from tool %s. Re-execute the tool if the full output is needed.]",
				c.index+1, (c.originalSize+1023)/1024, c.toolName,
			)
			results[c.index].Result = suppressMsg
			// Use ToolCallID if available, otherwise derive the same fallback key
			stateKey := results[c.index].ToolCallID
			if stateKey == "" {
				stateKey = fmt.Sprintf("%s_%d", results[c.index].ToolName, c.index)
			}
			e.state.MarkReplaced(stateKey, suppressMsg)
			remaining = remaining - currentSize + len(suppressMsg)
		}
	}

	return results
}
