//go:build knownissue

// 已知缺陷期望行为测试（agents 包）。
// 默认不编译；`go test -tags=knownissue ./agents` 会列出当前未修复项。
package agents

import (
	"context"
	"errors"
	"testing"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
)

type knownFailOnceStore struct {
	*fakeSessionStore
	failAt   int
	appended int
}

func (s *knownFailOnceStore) Append(ctx context.Context, sid, agent, sponsor string, m session.Message) error {
	s.appended++
	if s.failAt > 0 && s.appended == s.failAt {
		return errors.New("injected append failure")
	}
	return s.fakeSessionStore.Append(ctx, sid, agent, sponsor, m)
}

// V-07：回写中断后重试，已成功的消息不得重复落库。
func TestKnownIssue_WriteBackNoDuplicates(t *testing.T) {
	store := &knownFailOnceStore{fakeSessionStore: newFakeSessionStore(), failAt: 2}
	s, err := session.New("a", "", "/tmp", store, logging.NewNopLogger())
	if err != nil {
		t.Fatal(err)
	}
	msgs := []gochatcore.Message{
		{Role: gochatcore.RoleAssistant, Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "first"}}},
		{Role: gochatcore.RoleAssistant, Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "second"}}},
		{Role: gochatcore.RoleAssistant, Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "third"}}},
	}
	br := &eventBridge{
		b:            &AskBuilder{session: s},
		rt:           &Runtime{logger: logging.NewNopLogger(), tokenUsageStore: session.NewNoopTokenUsageStore()},
		completeMsgs: msgs,
	}
	br.beginRun(0)
	br.writeBackNewMessages(s, 0)
	store.failAt = 0
	br.writeBackNewMessages(s, 0)

	count := 0
	for _, m := range s.All() {
		if m.Content == "first" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("故障重放导致 first 重复 %d 次", count)
	}
}
