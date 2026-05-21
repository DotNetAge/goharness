package reactor

import (
	"fmt"
	"strings"
)

// ────────────────────────────────────────────────────────────────────────────
// 类型定义
// ────────────────────────────────────────────────────────────────────────────

// StuckPattern 表示检测到的卡死模式类型。
type StuckPattern int

const (
	PatternToolLoop      StuckPattern = iota // 同一工具反复调用
	PatternErrorLoop                          // 相同错误反复出现
	PatternOscillation                        // 决策在两个状态间来回跳跃
	PatternNoProgress                         // 多轮无实质性进展
)

func (p StuckPattern) String() string {
	switch p {
	case PatternToolLoop:
		return "tool_loop"
	case PatternErrorLoop:
		return "error_loop"
	case PatternOscillation:
		return "oscillation"
	case PatternNoProgress:
		return "no_progress"
	default:
		return "unknown"
	}
}

// ToolCallSnapshot 记录一次工具调用的快照。
type ToolCallSnapshot struct {
	Name  string `json:"name"`
	Error string `json:"error"`
}

// IterationSnapshot 记录一轮 T-A-O 周期的快照。
type IterationSnapshot struct {
	Iteration int                `json:"iteration"`
	Decision  string             `json:"decision"`
	ToolCalls []ToolCallSnapshot `json:"tool_calls,omitempty"`
}

// StuckDiagnosis 是卡死分析的结果。
type StuckDiagnosis struct {
	Stuck       bool         `json:"stuck"`
	Pattern     StuckPattern `json:"pattern"`
	Description string       `json:"description"`
	NudgePrefix string       `json:"nudge_prefix"` // "Note" 或 "Warning"
	NudgeDetail string       `json:"nudge_detail"` // 具体的 nudge 消息内容
}

// StuckDetector 分析迭代历史，检测卡死模式。
type StuckDetector interface {
	// Analyze 检查所有历史迭代，返回诊断结果（nil = 无异常）。
	Analyze(history []IterationSnapshot) *StuckDiagnosis
	// Name 返回检测器名称，用于日志。
	Name() string
}

// ────────────────────────────────────────────────────────────────────────────
// 内置检测器
// ────────────────────────────────────────────────────────────────────────────

// ToolLoopDetector 检测同一工具被连续反复调用。
type ToolLoopDetector struct {
	Threshold int // 连续相同工具触发阈值（默认 3）
}

func (d *ToolLoopDetector) Name() string { return "tool_loop" }

func (d *ToolLoopDetector) Analyze(history []IterationSnapshot) *StuckDiagnosis {
	n := len(history)
	if n < d.Threshold {
		return nil
	}

	// 从最新的迭代开始，检查连续相同工具名
	var toolChain []string
	for i := n - 1; i >= 0; i-- {
		iter := history[i]
		if len(iter.ToolCalls) == 0 {
			break
		}
		// 取第一个工具调用名称作为该轮指纹
		name := iter.ToolCalls[0].Name
		if len(toolChain) > 0 && toolChain[len(toolChain)-1] != name {
			break // 工具名变了 → 链中断
		}
		toolChain = append(toolChain, name)
	}

	if len(toolChain) >= d.Threshold {
		return &StuckDiagnosis{
			Stuck:       true,
			Pattern:     PatternToolLoop,
			Description: fmt.Sprintf("%s called %d times consecutively", toolChain[0], len(toolChain)),
			NudgePrefix: "Note",
			NudgeDetail: fmt.Sprintf("You've called %s %d times in a row. Consider whether a different approach might work better.", toolChain[0], len(toolChain)),
		}
	}
	return nil
}

// ErrorLoopDetector 检测同一错误被连续触发。
type ErrorLoopDetector struct {
	Threshold int // 连续相同错误触发阈值（默认 2）
}

func (d *ErrorLoopDetector) Name() string { return "error_loop" }

func (d *ErrorLoopDetector) Analyze(history []IterationSnapshot) *StuckDiagnosis {
	n := len(history)
	if n < d.Threshold {
		return nil
	}

	var lastErr string
	count := 0

	// 从最新迭代开始向前扫描
	for i := n - 1; i >= 0; i-- {
		hadError := false
		for _, tc := range history[i].ToolCalls {
			if tc.Error != "" {
				hadError = true
				if tc.Error == lastErr {
					count++
				} else {
					lastErr = tc.Error
					count = 1
				}
				if count >= d.Threshold {
					return &StuckDiagnosis{
						Stuck:       true,
						Pattern:     PatternErrorLoop,
						Description: fmt.Sprintf("same error '%s' occurred %d times", truncateStr(lastErr, 80), count),
						NudgePrefix: "Note",
						NudgeDetail: fmt.Sprintf("The same error '%s' has occurred %d times. Check prerequisites or try a different approach.", truncateStr(lastErr, 80), count),
					}
				}
			}
		}
		if !hadError {
			// 该轮无错误 → 错误链终止
			break
		}
	}
	return nil
}

// OscillationDetector 检测决策在几个状态之间来回跳跃。
type OscillationDetector struct {
	Threshold int // 至少需要多少轮来检测交替模式（默认 4）
}

func (d *OscillationDetector) Name() string { return "oscillation" }

func (d *OscillationDetector) Analyze(history []IterationSnapshot) *StuckDiagnosis {
	n := len(history)
	if n < d.Threshold {
		return nil
	}

	// 检查最近 Threshold 轮的决策是否呈交替模式（A, B, A, B, ...）
	// 即相邻决策两两不等
	lastN := history[n-d.Threshold:]
	for i := 1; i < len(lastN); i++ {
		if lastN[i].Decision == lastN[i-1].Decision {
			return nil // 相邻相等 → 不是交替
		}
	}

	// 确认是两值交替还是多值交替
	unique := map[string]int{}
	for _, s := range lastN {
		unique[s.Decision]++
	}

	return &StuckDiagnosis{
		Stuck:       true,
		Pattern:     PatternOscillation,
		Description: fmt.Sprintf("decision oscillating between %d values for %d iterations", len(unique), d.Threshold),
		NudgePrefix: "Note",
		NudgeDetail: fmt.Sprintf("Your decisions have been oscillating between states for %d iterations. Pick a direction and commit to it.", d.Threshold),
	}
}

// NoProgressDetector 检测多轮未产出答案的情况。
type NoProgressDetector struct {
	Threshold int // 无答案轮数阈值（默认 5）
}

func (d *NoProgressDetector) Name() string { return "no_progress" }

func (d *NoProgressDetector) Analyze(history []IterationSnapshot) *StuckDiagnosis {
	n := len(history)
	if n < d.Threshold {
		return nil
	}

	// 从最新迭代倒推，统计连续非 answer 决策的轮数
	noAnswer := 0
	for i := n - 1; i >= 0; i-- {
		if history[i].Decision == "answer" || history[i].Decision == "" {
			break // 有答案或空决策 → 链终止
		}
		noAnswer++
	}

	if noAnswer >= d.Threshold {
		return &StuckDiagnosis{
			Stuck:       true,
			Pattern:     PatternNoProgress,
			Description: fmt.Sprintf("no answer produced in %d consecutive iterations", noAnswer),
			NudgePrefix: "Note",
			NudgeDetail: fmt.Sprintf("%d iterations without producing an answer. Consider wrapping up or using AskUser to clarify requirements.", noAnswer),
		}
	}
	return nil
}

// ────────────────────────────────────────────────────────────────────────────
// CompositeDetector 组合多个检测器，按注册顺序检查，返回首个命中。
// ────────────────────────────────────────────────────────────────────────────

type CompositeDetector struct {
	Detectors []StuckDetector
}

func (c *CompositeDetector) Name() string { return "composite" }

func (c *CompositeDetector) Analyze(history []IterationSnapshot) *StuckDiagnosis {
	for _, d := range c.Detectors {
		if diag := d.Analyze(history); diag != nil && diag.Stuck {
			return diag
		}
	}
	return nil
}

// NewDefaultStuckDetector 创建包含全部内置检测器的默认组合。
func NewDefaultStuckDetector() *CompositeDetector {
	return &CompositeDetector{
		Detectors: []StuckDetector{
			&ErrorLoopDetector{Threshold: 2},
			&ToolLoopDetector{Threshold: 3},
			&OscillationDetector{Threshold: 4},
			&NoProgressDetector{Threshold: 5},
		},
	}
}

// ────────────────────────────────────────────────────────────────────────────
// 辅助
// ────────────────────────────────────────────────────────────────────────────

func truncateStr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// buildNudgeMessage 构造注入对话的提示消息。
// nudgeCount: 同一模式已检测到的次数（1 = 首次提示，2 = 警告，3+ = 应终止）。
func buildNudgeMessage(diag *StuckDiagnosis, nudgeCount int) string {
	if nudgeCount >= 2 {
		return fmt.Sprintf("Warning: %s\nIf no progress next iteration, the system will terminate.", diag.NudgeDetail)
	}
	return fmt.Sprintf("Note: %s", strings.TrimSpace(diag.NudgeDetail))
}
