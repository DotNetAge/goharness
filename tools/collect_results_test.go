package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DotNetAge/goagent"
	"github.com/DotNetAge/goharness/logging"
)

// TestCollectResults_Cancellation 验证 CollectResults 在父 context 被取消时能及时返回。
//
// 回归背景：原实现对未落定任务的等待不响应取消（旧轮询实现曾用 context.WithoutCancel
// 剥离父 ctx 的取消信号），导致停止按钮（message.cancel）无法中断长等待，
// 会话队列被占住、后续消息全部排队。现实现仅剥离截止时间、保留取消信号。
func TestCollectResults_Cancellation(t *testing.T) {
	manager := goagent.DefaultRuntimeManager()
	// 登记 Pending 态任务（永不结算），CollectResults 应阻塞等待其 Done。
	if _, err := manager.Register("task-cancel-test"); err != nil {
		t.Fatalf("登记任务失败: %v", err)
	}
	defer manager.Unregister("task-cancel-test")

	ctx, cancel := context.WithCancel(WithToolContext(context.Background(), &ToolContext{
		Logger: logging.NewNopLogger(),
	}))
	defer cancel()

	tool := NewCollectResultsTool()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = tool.Execute(ctx, map[string]any{"task_ids": []any{"task-cancel-test"}})
	}()

	// 等待进入等待期后取消（覆盖「等待中取消」的真实时序）
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// 及时返回，符合预期
	case <-time.After(10 * time.Second):
		t.Fatal("CollectResults 应在父 context 取消后及时返回，而非无限等待")
	}
}

// TestCollectResults_CompletedAndFailed 验证按跟踪句柄读取控制平面的三种结算路径：
// Completed → result；Failed → error（携带原因）；句柄不存在 → 引导重派。
func TestCollectResults_CompletedAndFailed(t *testing.T) {
	manager := goagent.DefaultRuntimeManager()
	if _, err := manager.Register("task-ok"); err != nil {
		t.Fatalf("登记任务失败: %v", err)
	}
	manager.Complete("task-ok", "分析完成：共 3 处问题")
	if _, err := manager.Register("task-bad"); err != nil {
		t.Fatalf("登记任务失败: %v", err)
	}
	manager.Fail("task-bad", "授权超时")

	tool := NewCollectResultsTool()
	out, err := tool.Execute(WithToolContext(context.Background(), &ToolContext{
		Logger: logging.NewNopLogger(),
	}), map[string]any{
		"task_ids": []any{"task-ok", "task-bad", "task-gone"},
	})
	if err != nil {
		t.Fatalf("Execute 不应报错，实际: %v", err)
	}
	payload, ok := out.(string)
	if !ok {
		t.Fatalf("Execute 应返回 JSON 字符串，实际: %T", out)
	}
	for _, want := range []string{
		`"task_id":"task-ok"`,
		`"status":"completed"`,
		`"result":"分析完成：共 3 处问题"`,
		`"task_id":"task-bad"`,
		`"error":"子代理任务失败: 授权超时"`,
		`"task_id":"task-gone"`,
		"任务句柄不存在",
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("结果应包含 %s，实际: %s", want, payload)
		}
	}

	manager.Unregister("task-ok")
	manager.Unregister("task-bad")
}

// TestWithoutDeadline 验证 withoutDeadline 的契约：
// 剥离截止时间、保留取消信号与上下文值。
func TestWithoutDeadline(t *testing.T) {
	base, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	wd := withoutDeadline(base)

	// 截止时间必须被剥离（这是 withoutDeadline 的用途：突破单次工具执行超时）
	if dl, ok := wd.Deadline(); ok {
		t.Errorf("withoutDeadline 应剥离截止时间，得到 %v", dl)
	}

	// 取消信号必须保留（这是修复的关键：停止按钮必须能中断等待）
	cancel()
	select {
	case <-wd.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("withoutDeadline 应保留父 context 的取消信号")
	}
	if !errors.Is(wd.Err(), context.Canceled) {
		t.Errorf("Err() 应返回 context.Canceled，得到 %v", wd.Err())
	}
}
