package session

import (
	"context"
	"testing"
)

// 回归：纯图片消息必须落库（用户在 MindX 里只发图、不带文字是合法输入）。
func TestAppend_KeepsImageOnlyMessage(t *testing.T) {
	s := newTestSession("append-image", "agent", newMockStore())
	msg := Message{Role: "user", Images: []ImageBlock{{MediaType: "image/png", Base64Data: "AAAA"}}}
	if err := s.Append(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	all := s.All()
	if len(all) != 1 || len(all[0].Images) != 1 {
		t.Fatalf("图片消息被丢弃: %+v", all)
	}
}

// 回归：空内容 tool 结果必须落库，否则 tool_calls 配对断裂。
func TestAppend_KeepsEmptyToolMessage(t *testing.T) {
	s := newTestSession("append-tool", "agent", newMockStore())
	msg := Message{Role: "tool", ToolCallID: "call_1", Content: ""}
	if err := s.Append(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	all := s.All()
	if len(all) != 1 || all[0].ToolCallID != "call_1" {
		t.Fatalf("空 tool 结果被丢弃: %+v", all)
	}
}

// 边界：无文本、无图片、无工具调用的消息仍按空消息丢弃。
func TestAppend_DropsTrulyEmptyMessage(t *testing.T) {
	s := newTestSession("append-empty", "agent", newMockStore())
	_ = s.Append(context.Background(),
		Message{Role: "user", Content: "   "},
		Message{Role: "assistant", ReasoningContent: "thinking only"},
	)
	if got := len(s.All()); got != 0 {
		t.Fatalf("空消息未被丢弃: %d", got)
	}
}

// 既有行为：普通消息、带 tool_calls 的 assistant、图文混合消息不受过滤改动影响。
func TestAppend_KeepsRegularMessages(t *testing.T) {
	s := newTestSession("append-regular", "agent", newMockStore())
	err := s.Append(context.Background(),
		Message{Role: "user", Content: "hello"},
		Message{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "call_1", Name: "Read"}}},
		Message{Role: "tool", Content: "file content", ToolCallID: "call_1"},
		Message{Role: "user", Content: "看图", Images: []ImageBlock{{MediaType: "image/png", Base64Data: "AAAA"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(s.All()); got != 4 {
		t.Fatalf("普通消息被误过滤: %d", got)
	}
}
