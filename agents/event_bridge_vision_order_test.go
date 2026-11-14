package agents

import (
	"testing"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/session"
)

// 并行工具调用都返回图片时，图片视觉消息必须排在本轮全部 tool 消息之后。
//
// 修复前的序列是 tool → user(图1) → tool → user(图2)：
// assistant 声明的多个 tool_calls 必须由连续的 tool 消息逐一回应，中间插入
// user 会被 OpenAI 兼容接口以 400 拒绝（insufficient tool messages），
// 导致该会话后续所有压缩/摘要请求失败。
func TestWriteBack_ParallelToolImagesStayAfterAllToolMessages(t *testing.T) {
	store := &rollbackFailStore{fakeSessionStore: newFakeSessionStore()}
	msgs := []gochatcore.Message{
		{
			Role: gochatcore.RoleAssistant,
			Content: []gochatcore.ContentBlock{
				{Type: gochatcore.ContentTypeText, Text: "读两张图"},
			},
			ToolCalls: []gochatcore.ToolCall{
				{ID: "call_1", Name: "Read"},
				{ID: "call_2", Name: "Read"},
			},
		},
		{
			Role:       gochatcore.RoleTool,
			ToolCallID: "call_1",
			Content:    []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "img1"}},
		},
		{
			Role:       gochatcore.RoleTool,
			ToolCallID: "call_2",
			Content:    []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "img2"}},
		},
	}

	br, s := newRollbackBridge(t, store, msgs)
	br.coord = &hookCoord{imageEntries: []imageEntry{
		{toolSeq: 0, toolName: "Read", blocks: []session.ImageBlock{{MediaType: "image/png", Base64Data: "AAAA"}}},
		{toolSeq: 1, toolName: "Read", blocks: []session.ImageBlock{{MediaType: "image/png", Base64Data: "BBBB"}}},
	}}

	br.writeBackNewMessages(s, 0)

	var roles []string
	for _, m := range s.All() {
		roles = append(roles, m.Role)
	}

	want := []string{"assistant", "tool", "tool", "user", "user"}
	if len(roles) != len(want) {
		t.Fatalf("消息条数不符：期望 %v，实际 %v", want, roles)
	}
	for i := range want {
		if roles[i] != want[i] {
			t.Fatalf("消息顺序不符（tool 响应被图片消息隔开）：期望 %v，实际 %v", want, roles)
		}
	}

	// 关键断言：assistant 之后必须紧跟两条 tool，中间不得有 user
	if roles[1] != "tool" || roles[2] != "tool" {
		t.Fatalf("assistant 的 tool_calls 必须由连续的 tool 消息回应，实际 %v", roles)
	}
}
