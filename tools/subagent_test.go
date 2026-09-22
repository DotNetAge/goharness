package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DotNetAge/goagent/subagent"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
)

// fakeDispatcher 模拟宿主受理方：受理行为由 submit 注入。
type fakeDispatcher struct {
	submit func(ctx context.Context, req subagent.SubAgentRequest) (subagent.SubAgentReceipt, error)

	reqs []subagent.SubAgentRequest
}

func (d *fakeDispatcher) Submit(ctx context.Context, req subagent.SubAgentRequest) (subagent.SubAgentReceipt, error) {
	d.reqs = append(d.reqs, req)
	return d.submit(ctx, req)
}

func newSubAgentTestCtx() context.Context {
	return WithToolContext(context.Background(), &ToolContext{
		Logger:    logging.NewNopLogger(),
		EmitEvent: func(e events.ReactEvent) {},
	})
}

// TestSubAgentTool_AcceptedDelegation 验证受理成功：工具委托 Dispatcher.Submit，
// 回执中的跟踪句柄（task_id）原样返回给模型，参数完整透传。
func TestSubAgentTool_AcceptedDelegation(t *testing.T) {
	d := &fakeDispatcher{submit: func(ctx context.Context, req subagent.SubAgentRequest) (subagent.SubAgentReceipt, error) {
		return subagent.SubAgentReceipt{Accepted: true, TaskID: "task-abc123"}, nil
	}}
	tool := NewSubAgentTool(d)

	res, err := tool.Execute(newSubAgentTestCtx(), map[string]any{
		"agent_name": "code_reviewer",
		"task":       "审查代码",
	})
	if err != nil {
		t.Fatalf("受理成功应无错误，实际: %v", err)
	}
	result, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("Execute 应返回 map[string]any，实际: %T", res)
	}
	if result["status"] != "running" {
		t.Fatalf("status 应为 running，实际: %v", result["status"])
	}
	if result["task_id"] != "task-abc123" {
		t.Fatalf("task_id 应为回执跟踪句柄，实际: %v", result["task_id"])
	}
	if len(d.reqs) != 1 || d.reqs[0].AgentName != "code_reviewer" || d.reqs[0].Task != "审查代码" {
		t.Fatalf("参数应完整透传给 Dispatcher，实际: %+v", d.reqs)
	}
}

// TestSubAgentTool_RejectedDelegation 验证策略性拒绝：回执携带的原因以柔性错误
// 返回，模型本轮即可自纠。
func TestSubAgentTool_RejectedDelegation(t *testing.T) {
	d := &fakeDispatcher{submit: func(ctx context.Context, req subagent.SubAgentRequest) (subagent.SubAgentReceipt, error) {
		return subagent.SubAgentReceipt{Accepted: false, Reason: "并发上限已满"}, nil
	}}
	tool := NewSubAgentTool(d)

	_, err := tool.Execute(newSubAgentTestCtx(), map[string]any{
		"agent_name": "worker",
		"task":       "任务",
	})
	if err == nil {
		t.Fatal("策略性拒绝应以错误返回")
	}
	if !strings.Contains(err.Error(), "并发上限已满") {
		t.Fatalf("拒绝原因应回传给模型，得到 %v", err)
	}
}

// TestSubAgentTool_SubmitError 验证受理过程失败（error 非拒绝）同步反馈给模型。
func TestSubAgentTool_SubmitError(t *testing.T) {
	d := &fakeDispatcher{submit: func(ctx context.Context, req subagent.SubAgentRequest) (subagent.SubAgentReceipt, error) {
		return subagent.SubAgentReceipt{}, errors.New("受理通道不可用")
	}}
	tool := NewSubAgentTool(d)

	_, err := tool.Execute(newSubAgentTestCtx(), map[string]any{
		"agent_name": "worker",
		"task":       "任务",
	})
	if err == nil {
		t.Fatal("受理失败应以错误返回")
	}
	if !strings.Contains(err.Error(), "受理通道不可用") {
		t.Fatalf("原始错误应包含在反馈中，得到 %v", err)
	}
}

// TestSubAgentTool_MissingParams 验证必填参数缺失时同步拒绝，不打扰 Dispatcher。
func TestSubAgentTool_MissingParams(t *testing.T) {
	d := &fakeDispatcher{submit: func(ctx context.Context, req subagent.SubAgentRequest) (subagent.SubAgentReceipt, error) {
		return subagent.SubAgentReceipt{Accepted: true, TaskID: "task-x"}, nil
	}}
	tool := NewSubAgentTool(d)

	if _, err := tool.Execute(newSubAgentTestCtx(), map[string]any{"task": "x"}); err == nil {
		t.Fatal("缺少 agent_name 应报错")
	}
	if _, err := tool.Execute(newSubAgentTestCtx(), map[string]any{"agent_name": "w"}); err == nil {
		t.Fatal("缺少 task 应报错")
	}
	if len(d.reqs) != 0 {
		t.Fatalf("校验失败不应到达 Dispatcher，实际收到 %d", len(d.reqs))
	}
}
