package agents

import (
	"strings"
	"testing"

	goagent "github.com/DotNetAge/goagent"
	gochatcore "github.com/DotNetAge/gochat/core"
)

// 回归用例：钩子「往 SystemPromptSections 末尾 append 一条 system 消息」的注入方式
// 不会生效——writeBackSystemSections 只能覆盖内核里已存在的 system 消息，
// 多出来的尾部段没有槽位，被静默丢弃。
//
// 这正是任务板提醒第一版的写法（system 段追加），当时看起来“注入了”，实际
// 模型从来收不到。提醒/视觉附件这类兜底注入必须走 HookResult.AppendMessages
// 追加真实消息到对话尾部。
func TestAppendSystemSectionIsDroppedByWriteBack(t *testing.T) {
	msgs := []gochatcore.Message{
		gochatcore.NewSystemMessage("系统提示 A"),
		gochatcore.NewUserMessage("用户问题"),
	}
	adapter := &loopHookAdapter{}
	in := adapter.buildCallInput(goagent.BeforeLLMInput{Iteration: 0, Messages: msgs})
	if len(in.SystemPromptSections) != 1 {
		t.Fatalf("内核只有 1 条 system 消息，期望 1，得到 %d", len(in.SystemPromptSections))
	}

	// 模拟第一版钩子：往 system 段末尾追加一条提醒。
	nudge := gochatcore.NewSystemMessage("【任务板提醒】距上次回写任务板已过去 8 次工具调用")
	in.SystemPromptSections = append(in.SystemPromptSections, nudge)

	adapter.writeBackSystemSections(msgs, in.SystemPromptSections)

	// 内核消息序列长度不变，且提醒正文没有出现在任何一条消息里。
	if len(msgs) != 2 {
		t.Fatalf("写回不应改变消息条数，期望 2，得到 %d", len(msgs))
	}
	for i, m := range msgs {
		if strings.Contains(m.TextContent(), "任务板提醒") {
			t.Fatalf("第 %d 条消息出现了提醒正文，说明尾部 system 段真的进了请求", i)
		}
	}
}
