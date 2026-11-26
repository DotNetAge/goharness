package loop

import (
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/session"
)

func assistantWithCalls(names ...string) session.Message {
	calls := make([]session.ToolCall, 0, len(names))
	for i, n := range names {
		calls = append(calls, session.ToolCall{ID: n + string(rune('a'+i)), Name: n})
	}
	return session.Message{Role: "assistant", ToolCalls: calls}
}

func TestScanTaskBoard(t *testing.T) {
	// 无任务板：任何工具调用都算不出「未回写」。
	hasBoard, since := scanTaskBoard([]session.Message{
		assistantWithCalls("Read", "Grep"),
	})
	if hasBoard || since != 0 {
		t.Fatalf("无板时应为 false/0，得到 %v/%d", hasBoard, since)
	}

	// 建板后连做 3 次工具调用，未回写 → since=3。
	hasBoard, since = scanTaskBoard([]session.Message{
		assistantWithCalls("TaskCreate", "TaskCreate"),
		assistantWithCalls("Read", "Edit"),
		assistantWithCalls("Bash"),
	})
	if !hasBoard || since != 3 {
		t.Fatalf("期望 true/3，得到 %v/%d", hasBoard, since)
	}

	// 回写后重新计数：只算最后一次回写之后。
	hasBoard, since = scanTaskBoard([]session.Message{
		assistantWithCalls("TaskCreate"),
		assistantWithCalls("Read", "Edit"),
		assistantWithCalls("TaskUpdate"),
		assistantWithCalls("Bash", "Bash"),
	})
	if !hasBoard || since != 2 {
		t.Fatalf("期望 true/2，得到 %v/%d", hasBoard, since)
	}

	// 只读任务板不算回写（TaskList 不推进状态）。
	hasBoard, since = scanTaskBoard([]session.Message{
		assistantWithCalls("TaskCreate"),
		assistantWithCalls("TaskList"),
	})
	if !hasBoard || since != 1 {
		t.Fatalf("TaskList 不应重置计数，期望 true/1，得到 %v/%d", hasBoard, since)
	}
}

func TestTaskBoardNudgeBeforeLLM(t *testing.T) {
	nudge := NewTaskBoardNudge(3)

	t.Run("无任务板不提醒", func(t *testing.T) {
		in := &hooks.CallInput{History: []session.Message{assistantWithCalls("Read", "Bash", "Grep")}}
		r := nudge.BeforeLLM("s0", 1, in)
		if r.IsTerminal() || len(in.SystemPromptSections) != 0 {
			t.Fatalf("不应注入：%v %d", r, len(in.SystemPromptSections))
		}
	})

	t.Run("建板但久未回写则注入一条提醒", func(t *testing.T) {
		in := &hooks.CallInput{History: []session.Message{
			assistantWithCalls("TaskCreate"),
			assistantWithCalls("Read", "Edit", "Bash"),
		}}
		nudge.BeforeLLM("s1", 1, in)
		if len(in.SystemPromptSections) != 1 {
			t.Fatalf("应注入 1 条，得到 %d", len(in.SystemPromptSections))
		}
		var text strings.Builder
		for _, blk := range in.SystemPromptSections[0].Content {
			text.WriteString(blk.Text)
		}
		if !strings.Contains(text.String(), "任务板提醒") {
			t.Fatalf("提醒正文异常: %q", text.String())
		}
	})

	t.Run("同一档位不重复提醒", func(t *testing.T) {
		history := []session.Message{
			assistantWithCalls("TaskCreate"),
			assistantWithCalls("Read", "Edit", "Bash"),
		}
		in1 := &hooks.CallInput{History: history}
		nudge.BeforeLLM("s2", 1, in1)
		in2 := &hooks.CallInput{History: history}
		nudge.BeforeLLM("s2", 2, in2)
		if len(in2.SystemPromptSections) != 0 {
			t.Fatalf("同档位不该再注入，得到 %d", len(in2.SystemPromptSections))
		}
	})

	t.Run("多跑一轮后可以再次提醒", func(t *testing.T) {
		in := &hooks.CallInput{History: []session.Message{
			assistantWithCalls("TaskCreate"),
			assistantWithCalls("Read", "Edit", "Bash"), // 第 1 档：3
			assistantWithCalls("Read", "Edit", "Bash"), // 第 2 档：6
		}}
		nudge.BeforeLLM("s3", 1, in)
		if len(in.SystemPromptSections) != 1 {
			t.Fatalf("应注入 1 条，得到 %d", len(in.SystemPromptSections))
		}
	})
}
