package agents

import (
	"testing"

	goagent "github.com/DotNetAge/goagent"
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/session"
)

// 来源标记必须穿过 session → gochat 内核消息的转换：回写落库时靠它区分
// 宿主注入与用户发言。漏掉这一层，任务板提醒会以一条没有来源的 user 消息
// 落库，前端把它当成用户提问、多切一轮并渲染成用户气泡。
func TestSessionMessageToGochatKeepsOrigin(t *testing.T) {
	cases := []struct {
		name string
		msg  session.Message
	}{
		{"纯文本提醒", session.Message{
			Role: "user", Content: "【任务板提醒】…", Source: session.SourceTaskBoardNudge,
		}},
		{"带图视觉附件", session.Message{
			Role: "user", Content: session.ToolVisionPrefix, Source: session.SourceToolVision,
			Images: []session.ImageBlock{{MediaType: "image/png", Base64Data: "AAAA"}},
		}},
		{"真实用户发言", session.Message{Role: "user", Content: "帮我看看这张图"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, ok := sessionMessageToGochat(c.msg)
			if !ok {
				t.Fatalf("转换失败")
			}
			if out.Origin != c.msg.Source {
				t.Fatalf("来源标记丢失：期望 %q，得到 %q", c.msg.Source, out.Origin)
			}
		})
	}
}

// 反向：投影回 CallInput.History 时来源与图片都要带上，且注入消息不覆盖
// UserMessage（否则钩子会把宿主文案当成用户本轮提问）。
func TestBuildCallInputProjectionKeepsSourceAndUserMessage(t *testing.T) {
	adapter := &loopHookAdapter{sid: "s1"}
	text := func(s string) []gochatcore.ContentBlock {
		return []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: s}}
	}
	input := goagent.BeforeLLMInput{Messages: []gochatcore.Message{
		gochatcore.NewUserMessage("真正的提问"),
		{Role: "user", Content: text(session.ToolVisionPrefix), Origin: session.SourceToolVision},
		{Role: "user", Content: text("【任务板提醒】…"), Origin: session.SourceTaskBoardNudge},
	}}
	call := adapter.buildCallInput(input)

	if call.UserMessage != "真正的提问" {
		t.Fatalf("UserMessage 应是真实提问（注入消息不得覆盖），得到 %q", call.UserMessage)
	}
	var vision, nudge bool
	for _, h := range call.History {
		if h.Source == session.SourceToolVision {
			vision = true
		}
		if h.Source == session.SourceTaskBoardNudge {
			nudge = true
		}
	}
	if !vision || !nudge {
		t.Fatalf("投影历史应保留来源标记：vision=%v nudge=%v", vision, nudge)
	}
}
