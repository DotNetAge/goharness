package agents

import (
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/logging"
)

// TestRecordIterationOutcome 连续超限失败计数的三分支语义：
// 正常完成清零、max_iterations 递增、其他失败清零（「连续」被中断）。
func TestRecordIterationOutcome(t *testing.T) {
	m := newSubAgentManager(&Runtime{logger: logging.DefaultLogger()})
	st := &perSessionState{}

	// 连续两次超限失败 → 递增到 2
	m.recordIterationOutcome(st, "max_iterations", "max_iterations")
	m.recordIterationOutcome(st, "max_iterations", "max_iterations")
	if st.iterationFailStreak != 2 {
		t.Fatalf("连续超限应递增计数，实际 %d", st.iterationFailStreak)
	}

	// 其他原因失败 → 「连续」被中断，清零
	m.recordIterationOutcome(st, "permission_timeout", "permission_timeout")
	if st.iterationFailStreak != 0 {
		t.Fatalf("其他失败类型应清零计数，实际 %d", st.iterationFailStreak)
	}

	// 超限失败 → execErr 情形（failureReason 为错误文本、termReason 仍为
	// max_iterations）同样递增
	m.recordIterationOutcome(st, "max_iterations", "sub-agent x: run error")
	if st.iterationFailStreak != 1 {
		t.Fatalf("超限失败（错误文本形式）应递增，实际 %d", st.iterationFailStreak)
	}

	// 正常完成 → 清零
	m.recordIterationOutcome(st, "", "")
	if st.iterationFailStreak != 0 {
		t.Fatalf("正常完成应清零计数，实际 %d", st.iterationFailStreak)
	}
}

// TestSubagentIterationFailConstants 熔断阈值与续跑指令的存在性与基本形态：
// 熔断阈值必须大于 1（至少给一次续跑机会）；
// 续跑指令必须引导「从断点继续」而非重做。
func TestSubagentIterationFailConstants(t *testing.T) {
	if subagentIterationFailLimit <= 1 {
		t.Errorf("熔断阈值 %d 过小：至少给一次续跑机会", subagentIterationFailLimit)
	}
	if !strings.Contains(subagentContinuationPrompt, "从中断处继续") {
		t.Error("续跑指令应引导从断点继续而非重做任务")
	}
	if strings.Contains(subagentContinuationPrompt, "重试") && !strings.Contains(subagentContinuationPrompt, "不要重复") {
		t.Error("续跑指令不得被理解为重做任务")
	}
}
