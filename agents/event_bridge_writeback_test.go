package agents

// writeBackNewMessages 的 Usage 回填回归测试：EvTokenUsage 按迭代号登记各次
// 调用的用量，回写时按批内 assistant 消息序号对位——第 k 条 assistant 携带
// 第 k 次调用的 usage，tool 消息不携带，回写后迭代用量表整体作废。

import (
	"context"
	"testing"
	"time"

	"github.com/DotNetAge/goagent"
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/events"
)

func TestWriteBackNewMessagesAttachesUsagePerAssistant(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	b := &AskBuilder{ctx: context.Background(), agentName: "test-agent", session: sess, runtime: rt}
	br := newEventBridge(rt, b, func(events.ReactEventType, any) {}, "conv-usage-test", time.Now(), nil)

	// 两次调用：迭代 0 产出带工具调用的 assistant，迭代 1 产出最终答案
	br.recordUsage(&goagent.TokenUsageEvent{
		Iteration: 0,
		Usage:     &gochatcore.Usage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110},
	})
	br.recordUsage(&goagent.TokenUsageEvent{
		Iteration: 1,
		Usage:     &gochatcore.Usage{PromptTokens: 200, CompletionTokens: 20, TotalTokens: 220},
	})

	br.completeMsgs = []gochatcore.Message{
		{
			Role:    gochatcore.RoleAssistant,
			Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "先查一下"}},
			ToolCalls: []gochatcore.ToolCall{{
				ID:        "call-1",
				Name:      "dummy",
				Arguments: "{}",
			}},
		},
		{
			Role:       gochatcore.RoleTool,
			ToolCallID: "call-1",
			Content:    []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "工具结果"}},
		},
		{
			Role:    gochatcore.RoleAssistant,
			Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "最终回答"}},
		},
	}
	br.writeBackNewMessages(sess, 0)

	msgs := sess.All()
	if len(msgs) != 3 {
		t.Fatalf("回写消息数不符: got %d, want 3", len(msgs))
	}
	if msgs[0].Usage == nil || msgs[0].Usage.PromptTokens != 100 || msgs[0].Usage.CompletionTokens != 10 {
		t.Errorf("第 0 条 assistant 应携带迭代 0 的 usage, got %+v", msgs[0].Usage)
	}
	if msgs[1].Usage != nil {
		t.Errorf("tool 消息不应携带 usage, got %+v", msgs[1].Usage)
	}
	if msgs[2].Usage == nil || msgs[2].Usage.PromptTokens != 200 || msgs[2].Usage.CompletionTokens != 20 {
		t.Errorf("第 1 条 assistant 应携带迭代 1 的 usage, got %+v", msgs[2].Usage)
	}
	if br.callUsageByIter != nil {
		t.Error("回写后迭代用量表应整体作废")
	}
}

// provider 未返回 usage 的调用：对位为空，其余消息的 usage 不受影响。
func TestWriteBackNewMessagesSkipsMissingUsage(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	b := &AskBuilder{ctx: context.Background(), agentName: "test-agent", session: sess, runtime: rt}
	br := newEventBridge(rt, b, func(events.ReactEventType, any) {}, "conv-usage-missing", time.Now(), nil)

	// 迭代 0 未返回 usage（recordUsage 直接跳过登记），迭代 1 正常
	br.recordUsage(&goagent.TokenUsageEvent{
		Iteration: 1,
		Usage:     &gochatcore.Usage{PromptTokens: 50, CompletionTokens: 5, TotalTokens: 55},
	})

	br.completeMsgs = []gochatcore.Message{
		{
			Role:    gochatcore.RoleAssistant,
			Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "第一条"}},
			ToolCalls: []gochatcore.ToolCall{{
				ID:        "call-1",
				Name:      "dummy",
				Arguments: "{}",
			}},
		},
		{
			Role:       gochatcore.RoleTool,
			ToolCallID: "call-1",
			Content:    []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "工具结果"}},
		},
		{
			Role:    gochatcore.RoleAssistant,
			Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "最终回答"}},
		},
	}
	br.writeBackNewMessages(sess, 0)

	msgs := sess.All()
	if len(msgs) != 3 {
		t.Fatalf("回写消息数不符: got %d, want 3", len(msgs))
	}
	if msgs[0].Usage != nil {
		t.Errorf("缺 usage 的调用对位应为空, got %+v", msgs[0].Usage)
	}
	if msgs[2].Usage == nil || msgs[2].Usage.PromptTokens != 50 {
		t.Errorf("第 1 条 assistant 应携带迭代 1 的 usage, got %+v", msgs[2].Usage)
	}
}
