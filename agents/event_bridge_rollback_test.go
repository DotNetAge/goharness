package agents

import (
	"context"
	"errors"
	"testing"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
)

type rollbackFailStore struct {
	*fakeSessionStore
	failAt   int
	appended int
}

func (s *rollbackFailStore) Append(ctx context.Context, sid, agent, sponsor string, m session.Message) error {
	s.appended++
	if s.failAt > 0 && s.appended == s.failAt {
		return errors.New("injected append failure")
	}
	return s.fakeSessionStore.Append(ctx, sid, agent, sponsor, m)
}

func newRollbackBridge(t *testing.T, store *rollbackFailStore, msgs []gochatcore.Message) (*eventBridge, *session.Session) {
	t.Helper()
	s, err := session.New("a", "", "/tmp", store, logging.NewNopLogger())
	if err != nil {
		t.Fatal(err)
	}
	br := &eventBridge{
		b:            &AskBuilder{session: s},
		rt:           &Runtime{logger: logging.NewNopLogger(), tokenUsageStore: session.NewNoopTokenUsageStore()},
		completeMsgs: msgs,
	}
	br.beginRun(0)
	return br, s
}

// 回归：回写中断后重试，已成功的消息不得重复落库。
func TestWriteBack_NoDuplicatesAfterFailure(t *testing.T) {
	store := &rollbackFailStore{fakeSessionStore: newFakeSessionStore(), failAt: 2}
	msgs := []gochatcore.Message{
		{Role: gochatcore.RoleAssistant, Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "first"}}},
		{Role: gochatcore.RoleAssistant, Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "second"}}},
		{Role: gochatcore.RoleAssistant, Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "third"}}},
	}
	br, s := newRollbackBridge(t, store, msgs)

	br.writeBackNewMessages(s, 0)
	store.failAt = 0
	br.writeBackNewMessages(s, 0)

	counts := map[string]int{}
	for _, m := range s.All() {
		counts[m.Content]++
	}
	if counts["first"] != 1 || counts["second"] != 1 || counts["third"] != 1 {
		t.Fatalf("重放导致重复或丢失: %v", counts)
	}
}

// 回归：tool 消息已落库、后续图片消息追加失败时，不得回退水位导致 tool 重复。
func TestWriteBack_ImageFailureDoesNotDuplicateTool(t *testing.T) {
	store := &rollbackFailStore{fakeSessionStore: newFakeSessionStore(), failAt: 3}
	msgs := []gochatcore.Message{
		{Role: gochatcore.RoleAssistant, Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "call"}},
			ToolCalls: []gochatcore.ToolCall{{ID: "call_1", Name: "Read"}}},
		{Role: gochatcore.RoleTool, ToolCallID: "call_1", Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "ok"}}},
	}
	br, s := newRollbackBridge(t, store, msgs)
	br.coord = &hookCoord{imageEntries: []imageEntry{{
		toolSeq: 0, toolName: "Read", blocks: []session.ImageBlock{{MediaType: "image/png", Base64Data: "AAAA"}},
	}}}

	br.writeBackNewMessages(s, 0) // assistant(1) + tool(2) 成功，图片(3) 失败
	store.failAt = 0
	br.writeBackNewMessages(s, 0)

	toolCount := 0
	for _, m := range s.All() {
		if m.Role == "tool" {
			toolCount++
		}
	}
	if toolCount != 1 {
		t.Fatalf("图片追加失败导致 tool 消息重复 %d 次", toolCount)
	}
}
