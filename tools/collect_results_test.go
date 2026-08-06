package tools

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
)

// TestCollectResults_Cancellation 验证 CollectResults 在父 context 被取消时能及时返回。
//
// 回归背景：原实现使用 context.WithoutCancel 剥离父 ctx 的取消信号与截止时间，
// 导致停止按钮（message.cancel）无法中断最长 30 分钟的轮询等待，会话队列被占住、
// 后续消息全部排队。修复后仅剥离截止时间、保留取消信号。
func TestCollectResults_Cancellation(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("获取工作目录失败: %v", err)
	}
	store := newMockSessionStore()
	sess, err := session.New("test-agent", "", cwd, store, logging.NewNopLogger())
	if err != nil {
		t.Fatalf("创建会话失败: %v", err)
	}
	base := WithToolContext(context.Background(), &ToolContext{
		Session:      sess,
		SessionStore: store,
		Logger:       logging.NewNopLogger(),
		EmitEvent:    func(e events.ReactEvent) {},
	})

	ctx, cancel := context.WithCancel(base)
	defer cancel()

	tool := NewCollectResultsTool()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = tool.Execute(ctx, map[string]any{"session_ids": []any{"sub-1"}})
	}()

	// 等待轮询进入等待期后取消（覆盖「轮询中取消」的真实时序）
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// 及时返回，符合预期
	case <-time.After(10 * time.Second):
		t.Fatal("CollectResults 应在父 context 取消后及时返回，而非等待 30 分钟轮询超时")
	}
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

	// 取消信号必须保留（这是修复的关键：停止按钮必须能中断轮询等待）
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
