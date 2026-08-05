package agents

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
)

// pairingMsg 构造测试消息。toolCallID 仅对 tool 角色消息生效；
// tcs 为 assistant 消息声明的工具调用列表。
func pairingMsg(role, content, toolCallID string, tcs ...session.ToolCall) session.Message {
	m := session.Message{
		Role:      role,
		Content:   content,
		Timestamp: time.Now().UnixNano(),
	}
	if toolCallID != "" {
		m.ToolCallID = toolCallID
	}
	m.ToolCalls = tcs
	return m
}

func pairingTC(id, name string) session.ToolCall {
	return session.ToolCall{ID: id, Name: name, Arguments: "{}"}
}

func TestFindToolPairingBreak(t *testing.T) {
	cases := []struct {
		name string
		win  []session.Message
		want int
	}{
		{
			name: "空窗口无需修复",
			win:  nil,
			want: -1,
		},
		{
			name: "单轮完整配对",
			win: []session.Message{
				pairingMsg("user", "问题", ""),
				pairingMsg("assistant", "", "", pairingTC("A", "Grep")),
				pairingMsg("tool", "结果A", "A"),
			},
			want: -1,
		},
		{
			name: "多轮完整配对",
			win: []session.Message{
				pairingMsg("user", "问题", ""),
				pairingMsg("assistant", "", "", pairingTC("A", "Grep")),
				pairingMsg("tool", "结果A", "A"),
				pairingMsg("assistant", "继续", "", pairingTC("B", "Read")),
				pairingMsg("tool", "结果B", "B"),
				pairingMsg("user", "补充", ""),
			},
			want: -1,
		},
		{
			name: "纯文本轮次无需修复",
			win: []session.Message{
				pairingMsg("user", "问题", ""),
				pairingMsg("assistant", "直接回答", ""),
			},
			want: -1,
		},
		{
			name: "尾轮工具缺失截断到该轮user之后",
			win: []session.Message{
				pairingMsg("user", "问题", ""),
				pairingMsg("assistant", "", "", pairingTC("A", "Grep")),
				pairingMsg("tool", "结果A", "A"),
				pairingMsg("user", "继续", ""),
				pairingMsg("assistant", "", "", pairingTC("B", "Read")),
			},
			want: 4, // 保留 [user, asst(A), tool(A), user]，以 user 结尾
		},
		{
			name: "并发交错：靠前assistant的tool被隔开",
			win: []session.Message{
				pairingMsg("user", "问题", ""),
				pairingMsg("assistant", "", "", pairingTC("A", "Grep"), pairingTC("B", "Read")),
				pairingMsg("tool", "结果A", "A"),
				pairingMsg("assistant", "", "", pairingTC("C", "Bash")),
				pairingMsg("tool", "结果B", "B"),
			},
			want: 1, // 保留 [user]，后续全部回滚
		},
		{
			name: "断裂在开头：tool与assistant配对错位",
			win: []session.Message{
				pairingMsg("assistant", "", "", pairingTC("A", "Grep")),
				pairingMsg("tool", "结果B", "B"),
			},
			want: 0, // 全删，保留空窗口
		},
		{
			name: "中间工具完全缺失",
			win: []session.Message{
				pairingMsg("user", "问题", ""),
				pairingMsg("assistant", "", "", pairingTC("A", "Grep"), pairingTC("B", "Read")),
				pairingMsg("tool", "结果A", "A"),
				pairingMsg("user", "继续", ""),
			},
			want: 1, // asst(A,B) 缺 B，回退到 user 之后
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := findToolPairingBreak(c.win); got != c.want {
				t.Fatalf("findToolPairingBreak() = %d, want %d", got, c.want)
			}
		})
	}
}

func TestIsToolPairingError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil 不匹配", err: nil, want: false},
		{
			name: "配对错误特征命中",
			err:  fmt.Errorf("api_error: request failed with status 400: An assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'. (insufficient tool messages following tool_calls message)"),
			want: true,
		},
		{name: "普通错误不匹配", err: fmt.Errorf("request timeout"), want: false},
		{name: "其他400不匹配", err: fmt.Errorf("invalid tool_call_id"), want: false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isToolPairingError(c.err); got != c.want {
				t.Fatalf("isToolPairingError() = %v, want %v", got, c.want)
			}
		})
	}
}

// newPairingTestSession 创建绑定 fake store 的测试会话，返回会话与存储句柄。
func newPairingTestSession(t *testing.T) (*session.Session, *fakeSessionStore) {
	t.Helper()
	store := newFakeSessionStore()
	sess, err := session.New("test-agent", "", "/tmp/project", store, logging.NewNopLogger())
	if err != nil {
		t.Fatalf("创建测试会话失败: %v", err)
	}
	store.ensureMeta(sess)
	return sess, store
}

func TestRepairToolPairingBreak_截断并同步持久化(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, store := newPairingTestSession(t)

	// 坏序列：尾轮 assistant 声明 B 但缺少 tool(B)
	msgs := []session.Message{
		pairingMsg("user", "问题", ""),
		pairingMsg("assistant", "", "", pairingTC("A", "Grep")),
		pairingMsg("tool", "结果A", "A"),
		pairingMsg("user", "继续", ""),
		pairingMsg("assistant", "", "", pairingTC("B", "Read")),
	}
	if err := sess.Append(ctx, msgs...); err != nil {
		t.Fatalf("追加消息失败: %v", err)
	}

	if !repairToolPairingBreak(ctx, sess, logging.NewNopLogger()) {
		t.Fatalf("repairToolPairingBreak() 应返回 true")
	}

	// 截断后窗口应为 [user, asst(A), tool(A), user]
	got := sess.Current()
	if len(got) != 4 {
		t.Fatalf("截断后窗口消息数 = %d, want 4", len(got))
	}
	if got[len(got)-1].Role != "user" {
		t.Fatalf("截断后窗口末尾角色 = %q, want user（无需追加说明）", got[len(got)-1].Role)
	}

	// 存储应同步截断
	stored, err := store.Get(ctx, sess.ID())
	if err != nil {
		t.Fatalf("读取存储失败: %v", err)
	}
	if len(stored) != 4 {
		t.Fatalf("存储中消息数 = %d, want 4（Truncate 必须同步持久化）", len(stored))
	}
}

func TestRepairToolPairingBreak_空窗口时追加说明(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, _ := newPairingTestSession(t)

	// 断裂在开头（tool 与 assistant 配对错位）→ 全删 → 窗口为空 → 追加说明消息
	msgs := []session.Message{
		pairingMsg("assistant", "", "", pairingTC("A", "Grep")),
		pairingMsg("tool", "结果B", "B"),
	}
	if err := sess.Append(ctx, msgs...); err != nil {
		t.Fatalf("追加消息失败: %v", err)
	}

	if !repairToolPairingBreak(ctx, sess, logging.NewNopLogger()) {
		t.Fatalf("repairToolPairingBreak() 应返回 true")
	}

	got := sess.Current()
	if len(got) != 1 {
		t.Fatalf("追加说明后消息数 = %d, want 1", len(got))
	}
	if got[0].Role != "user" || got[0].Content != toolPairingRepairNotice {
		t.Fatalf("说明消息不符: role=%q content=%q", got[0].Role, got[0].Content)
	}
}

func TestRepairToolPairingBreak_窗口完整返回false(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, store := newPairingTestSession(t)

	msgs := []session.Message{
		pairingMsg("user", "问题", ""),
		pairingMsg("assistant", "", "", pairingTC("A", "Grep")),
		pairingMsg("tool", "结果A", "A"),
	}
	if err := sess.Append(ctx, msgs...); err != nil {
		t.Fatalf("追加消息失败: %v", err)
	}

	if repairToolPairingBreak(ctx, sess, logging.NewNopLogger()) {
		t.Fatalf("窗口完整时 repairToolPairingBreak() 应返回 false")
	}

	// 消息应保持不变
	stored, err := store.Get(ctx, sess.ID())
	if err != nil {
		t.Fatalf("读取存储失败: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("完整窗口不应被修改，消息数 = %d, want 3", len(stored))
	}
}
