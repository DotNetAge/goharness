package agents

import (
	"testing"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/session"
)

// 并行工具调用都返回图片时，图片视觉附件必须排在本轮全部 tool 消息之后。
//
// 顺序约束来自 OpenAI 兼容协议：assistant 声明的多个 tool_calls 必须由连续的
// tool 消息逐一回应，中间插入 user 会被 400 拒绝（insufficient tool messages），
// 导致该会话后续所有请求失败。
//
// 现在图片由循环钩子在**下一次请求前**追加到对话尾部（takeVisionMessages），
// 落库时它必然位于全部 tool 之后，天然满足该约束；本用例同时验证追加出的
// 消息带来源标记，不会被前端当成用户发言。
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

	// 模拟循环钩子在下一次请求前把两张图追加到对话尾部。
	adapter := &loopHookAdapter{coord: br.coord}
	appended := adapter.takeVisionMessages()
	if len(appended) != 2 {
		t.Fatalf("应追加 2 条图片消息，得到 %d", len(appended))
	}
	msgs = append(msgs, appended...)
	br.completeMsgs = msgs

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

	// 追加的图片消息必须带图、带来源标记、且不以「以下是…」的用户口吻开头。
	last := s.All()[len(s.All())-1]
	if len(last.Images) != 1 {
		t.Fatalf("图片消息应携带图片块，得到 %d", len(last.Images))
	}
	if last.Source != session.SourceToolVision {
		t.Fatalf("图片消息来源标记应为 %q，得到 %q", session.SourceToolVision, last.Source)
	}
	if !hasPrefix(last.Content, session.ToolVisionPrefix) {
		t.Fatalf("图片消息应以 %q 开头，得到 %q", session.ToolVisionPrefix, last.Content)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
