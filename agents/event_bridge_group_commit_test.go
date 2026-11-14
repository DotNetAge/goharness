package agents

import (
	"context"
	"testing"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
)

// groupRecordStore 记录每次批量写入的分组内容，用于断言「成对落盘」。
type groupRecordStore struct {
	*fakeSessionStore
	batches [][]session.Message
}

func (s *groupRecordStore) AppendBatch(ctx context.Context, sid, agent, sponsor string, msgs []session.Message) error {
	cp := make([]session.Message, len(msgs))
	copy(cp, msgs)
	s.batches = append(s.batches, cp)
	for _, m := range msgs {
		if err := s.fakeSessionStore.Append(ctx, sid, agent, sponsor, m); err != nil {
			return err
		}
	}
	return nil
}

func newGroupTestBridge(t *testing.T, store *groupRecordStore, msgs []gochatcore.Message) (*eventBridge, *session.Session) {
	t.Helper()
	s, err := session.New("a", "", "/tmp", store, logging.NewNopLogger())
	if err != nil {
		t.Fatalf("session.New: %v", err)
	}
	br := &eventBridge{
		b:            &AskBuilder{session: s},
		rt:           &Runtime{logger: logging.NewNopLogger(), tokenUsageStore: session.NewNoopTokenUsageStore()},
		completeMsgs: msgs,
	}
	br.beginRun(0)
	return br, s
}

// 核心不变式：一个 assistant(tool_calls) 与它欠的全部 tool 响应必须一次落盘。
// 分次写入会在磁盘上留下「有 tool_calls、无 tool 响应」的非法序列，
// 让该会话之后每次请求都被 400 拒绝。
func TestWriteBack_AssistantAndToolCommitsAsOneGroup(t *testing.T) {
	store := &groupRecordStore{fakeSessionStore: newFakeSessionStore()}
	msgs := []gochatcore.Message{
		{
			Role: gochatcore.RoleAssistant,
			Content: []gochatcore.ContentBlock{
				{Type: gochatcore.ContentTypeText, Text: "并行读两个文件"},
			},
			ToolCalls: []gochatcore.ToolCall{
				{ID: "call_1", Name: "Read"},
				{ID: "call_2", Name: "Read"},
			},
		},
		{
			Role:       gochatcore.RoleTool,
			ToolCallID: "call_1",
			Content:    []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "r1"}},
		},
		{
			Role:       gochatcore.RoleTool,
			ToolCallID: "call_2",
			Content:    []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "r2"}},
		},
	}

	br, s := newGroupTestBridge(t, store, msgs)
	br.writeBackNewMessages(s, 0)

	if len(store.batches) != 1 {
		t.Fatalf("应一次提交整组，实际提交 %d 次：%v", len(store.batches), batchSizes(store.batches))
	}
	if got := len(store.batches[0]); got != 3 {
		t.Fatalf("整组应含 assistant + 2 个 tool 共 3 条，实际 %d 条", got)
	}
	if store.batches[0][0].Role != "assistant" {
		t.Errorf("组首条应为 assistant，实际 %q", store.batches[0][0].Role)
	}

	// 磁盘上 assistant 之后必须紧跟两条 tool，中间不得插入其它角色
	all := s.All()
	if len(all) != 3 {
		t.Fatalf("落库条数不符：期望 3，实际 %d", len(all))
	}
	if all[0].Role != "assistant" || all[1].Role != "tool" || all[2].Role != "tool" {
		t.Errorf("落库顺序非法：%s,%s,%s", all[0].Role, all[1].Role, all[2].Role)
	}
}

// 组不完整（工具结果缺失）时整组不落库：
// 宁可丢一轮让用户重试，也不能留下会引发永久 400 的半组。
func TestWriteBack_IncompleteGroupNotPersisted(t *testing.T) {
	store := &groupRecordStore{fakeSessionStore: newFakeSessionStore()}
	msgs := []gochatcore.Message{
		{
			Role: gochatcore.RoleAssistant,
			Content: []gochatcore.ContentBlock{
				{Type: gochatcore.ContentTypeText, Text: "并行读两个文件"},
			},
			ToolCalls: []gochatcore.ToolCall{
				{ID: "call_1", Name: "Read"},
				{ID: "call_2", Name: "Read"},
			},
		},
		{
			Role:       gochatcore.RoleTool,
			ToolCallID: "call_1",
			Content:    []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "r1"}},
		},
		// call_2 的响应缺失（工具中断 / 进程退出）
	}

	br, s := newGroupTestBridge(t, store, msgs)
	br.writeBackNewMessages(s, 0)

	if len(store.batches) != 0 {
		t.Fatalf("不完整的组不应落库，实际提交 %d 次", len(store.batches))
	}
	if got := len(s.All()); got != 0 {
		t.Fatalf("会话中不应新增任何消息，实际 %d 条", got)
	}
	if br.writeLen != 0 {
		t.Errorf("writeLen 应停在组起点 0，实际 %d", br.writeLen)
	}
}

// 无 tool_calls 的 assistant 是单条组，应立即落库（正常回答不能被缓冲住）。
func TestWriteBack_PlainAssistantCommitsImmediately(t *testing.T) {
	store := &groupRecordStore{fakeSessionStore: newFakeSessionStore()}
	msgs := []gochatcore.Message{
		{
			Role:    gochatcore.RoleAssistant,
			Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "最终回答"}},
		},
	}

	br, s := newGroupTestBridge(t, store, msgs)
	br.writeBackNewMessages(s, 0)

	if len(store.batches) != 1 || len(store.batches[0]) != 1 {
		t.Fatalf("单条 assistant 应作为一组立即提交，实际 %v", batchSizes(store.batches))
	}
	if got := len(s.All()); got != 1 {
		t.Errorf("期望落库 1 条，实际 %d 条", got)
	}
}

func batchSizes(batches [][]session.Message) []int {
	out := make([]int, len(batches))
	for i, b := range batches {
		out[i] = len(b)
	}
	return out
}
