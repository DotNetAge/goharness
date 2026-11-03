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

// provider 未返回 usage 的调用：回写时按 DeepSeek 字符级公式对位估算补齐，
// 带 Estimated 标记；估算随对话增长（第 1 次调用的 prompt < 第 2 次）。
func TestWriteBackNewMessagesEstimatesMissingUsage(t *testing.T) {
	rt := newTestRuntime(t)
	sess := newTestSession(t)
	b := &AskBuilder{ctx: context.Background(), agentName: "test-agent", session: sess, runtime: rt}
	br := newEventBridge(rt, b, func(events.ReactEventType, any) {}, "conv-usage-estimate", time.Now(), nil)

	// 两次调用均未返回 usage（recordUsage 登记估算占位）
	br.recordUsage(&goagent.TokenUsageEvent{Iteration: 0})
	br.recordUsage(&goagent.TokenUsageEvent{Iteration: 1})

	br.completeMsgs = []gochatcore.Message{
		{
			Role:    gochatcore.RoleAssistant,
			Content: []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "第一条回复"}},
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
			Role:             gochatcore.RoleAssistant,
			Content:          []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: "最终回答内容更长的第二条回复"}},
			ReasoningContent: "一段思考流",
		},
	}
	br.writeBackNewMessages(sess, 0)

	msgs := sess.All()
	if len(msgs) != 3 {
		t.Fatalf("回写消息数不符: got %d, want 3", len(msgs))
	}
	u0, u1 := msgs[0].Usage, msgs[2].Usage
	if u0 == nil || !u0.Estimated || u0.CompletionTokens <= 0 {
		// baseLen=0 的夹具下第 0 次调用 prompt 无历史可估（真实运行 baseLen≥1），
		// 断言口径只要求 completion 有值且带估算标记
		t.Errorf("第 0 条 assistant 应携带估算 usage, got %+v", u0)
	}
	if u1 == nil || !u1.Estimated {
		t.Fatalf("第 1 条 assistant 应携带估算 usage, got %+v", u1)
	}
	if u1.PromptTokens <= u0.PromptTokens {
		t.Errorf("估算 prompt 应随对话窗口增长: iter0=%d iter1=%d", u0.PromptTokens, u1.PromptTokens)
	}
	if u1.TotalTokens != u1.PromptTokens+u1.CompletionTokens {
		t.Errorf("估算 total 应为 prompt+completion: got %d", u1.TotalTokens)
	}
	if br.callUsageByIter != nil {
		t.Error("回写后迭代用量表应整体作废")
	}
}
