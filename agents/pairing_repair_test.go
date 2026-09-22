package agents

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/events"
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

	repaired, _ := repairToolPairingBreak(ctx, sess, logging.NewNopLogger())
	if !repaired {
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

	repaired, _ := repairToolPairingBreak(ctx, sess, logging.NewNopLogger())
	if !repaired {
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

	if repaired, _ := repairToolPairingBreak(ctx, sess, logging.NewNopLogger()); repaired {
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

// TestRepairToolPairingBreak_挂起孤儿回补用户消息 验证高危回归修复（发现 1）：
// ask_user 挂起场景下会话遗留孤儿 assistant(tool_calls)（挂起轮回写、无配对
// tool 结果），恢复轮的用户回答与新问题依次追加其后。修复必须只回滚坏轮次的
// 孤儿 assistant，而把被截区间内的用户消息按原顺序回补——否则用户回答在持久层
// 丢失，下一轮 LLM 失忆且前端历史缺条。
func TestRepairToolPairingBreak_挂起孤儿回补用户消息(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, store := newPairingTestSession(t)

	// 坏序列：挂起孤儿 asst(AskUser) 后随用户回答与新问题
	msgs := []session.Message{
		pairingMsg("user", "初始问题", ""),
		pairingMsg("assistant", "", "", pairingTC("S", "AskUser")),
		pairingMsg("user", "用户回答", ""),
		pairingMsg("user", "新问题", ""),
	}
	if err := sess.Append(ctx, msgs...); err != nil {
		t.Fatalf("追加消息失败: %v", err)
	}

	repaired, removedTCs := repairToolPairingBreak(ctx, sess, logging.NewNopLogger())
	if !repaired {
		t.Fatalf("repairToolPairingBreak() 应返回 true")
	}
	if _, hit := removedTCs["S"]; !hit {
		t.Fatalf("removedTCs 应包含被截孤儿 tool_call S, got %v", removedTCs)
	}

	// 回补后窗口应为 [user(初始问题), user(用户回答), user(新问题)]：
	// 坏轮次的孤儿 assistant 回滚，用户消息按原顺序完整保留
	got := sess.Current()
	if len(got) != 3 {
		t.Fatalf("回补后窗口消息数 = %d, want 3", len(got))
	}
	for i, want := range []struct{ role, content string }{
		{"user", "初始问题"}, {"user", "用户回答"}, {"user", "新问题"},
	} {
		if got[i].Role != want.role || got[i].Content != want.content {
			t.Fatalf("回补后窗口[%d] = (%s, %q), want (%s, %q)",
				i, got[i].Role, got[i].Content, want.role, want.content)
		}
	}
	// 末尾已是 user 消息，不应误加说明消息
	if got[len(got)-1].Content == toolPairingRepairNotice {
		t.Fatalf("末尾已是用户消息，不应追加说明消息")
	}

	// 存储应同步截断+回补
	stored, err := store.Get(ctx, sess.ID())
	if err != nil {
		t.Fatalf("读取存储失败: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("存储中消息数 = %d, want 3（Truncate 与回补必须同步持久化）", len(stored))
	}
}

// TestInvalidatePendingsAfterRepair 验证挂起失效闭环（发现 3）：入口配对修复
// 回滚坏轮次后，命中被截 tool_call 的挂起授权/挂起提问必须失效——授权发射
// PermissionDenied 撤前端弹窗，提问丢弃；未命中的挂起提问原样保留。
func TestInvalidatePendingsAfterRepair(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	sess, _ := newPairingTestSession(t)
	rt := &Runtime{logger: logging.NewNopLogger()}
	b := &AskBuilder{ctx: ctx, session: sess}

	var denied []string
	emit := func(_ events.ReactEventType, data any) {
		if s, ok := data.(string); ok {
			denied = append(denied, s)
		}
	}

	// 场景一：挂起授权与挂起提问均命中被截集合 → 授权撤销并发射 PermissionDenied，提问丢弃
	sess.SetPendingPermission(session.PendingPermission{ToolName: "Bash", ToolCallID: "tc_gone"})
	sess.SetPendingAskUser(session.PendingAskUser{ToolCallID: "ask_gone", Question: "被回滚的提问"})
	rt.invalidatePendingsAfterRepair(b, map[string]struct{}{"tc_gone": {}, "ask_gone": {}}, emit)

	if sess.HasPendingPermission() {
		t.Fatal("命中被截集合的挂起授权应被撤销")
	}
	if sess.HasPendingAskUser() {
		t.Fatal("命中被截集合的挂起提问应被丢弃")
	}
	if len(denied) != 1 {
		t.Fatalf("应发射一次 PermissionDenied, got %v", denied)
	}

	// 场景二：挂起提问未命中被截集合 → 原样保留
	sess.SetPendingAskUser(session.PendingAskUser{ToolCallID: "ask_kept", Question: "仍然有效的提问"})
	rt.invalidatePendingsAfterRepair(b, map[string]struct{}{"tc_gone": {}, "ask_gone": {}}, emit)
	if p := sess.TakePendingAskUser(); p == nil || p.ToolCallID != "ask_kept" {
		t.Fatalf("未命中的挂起提问应保留, got %+v", p)
	}

	// 场景三：挂起提问缺失 ToolCallID（无法定位对应调用）→ 丢弃
	sess.SetPendingAskUser(session.PendingAskUser{Question: "无 ToolCallID 的提问"})
	rt.invalidatePendingsAfterRepair(b, nil, emit)
	if sess.HasPendingAskUser() {
		t.Fatal("缺失 ToolCallID 的挂起提问应被丢弃")
	}
	if len(denied) != 1 {
		t.Fatalf("无挂起授权时不应发射 PermissionDenied, got %v", denied)
	}
}
