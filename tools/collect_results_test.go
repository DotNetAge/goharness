package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/logging"
)

// fakeHarvester 记录 StartHarvest 调用，返回可配置的错误（nil = 启动成功）。
type fakeHarvester struct {
	calls    []harvestCall
	response error
}

type harvestCall struct {
	mainSessionID string
	targets       []HarvestTarget
}

func (f *fakeHarvester) StartHarvest(mainSessionID string, targets []HarvestTarget) error {
	f.calls = append(f.calls, harvestCall{mainSessionID: mainSessionID, targets: targets})
	return f.response
}

func collectToolContext() context.Context {
	return WithToolContext(context.Background(), &ToolContext{
		Logger:  logging.NewNopLogger(),
		Session: mockSession(),
	})
}

// TestCollectResults_NonBlockingReceipt 验证非阻塞契约：调用即返回
// harvesting 回执（含软提示），收割器被启动、目标按入参对位传递。
func TestCollectResults_NonBlockingReceipt(t *testing.T) {
	fake := &fakeHarvester{}
	tool := NewCollectResultsTool(fake)

	out, err := tool.Execute(collectToolContext(), map[string]any{
		"task_ids": []any{"task-a", "task-b"},
	})
	if err != nil {
		t.Fatalf("启动收割不应报错，实际: %v", err)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("应返回回执 map，实际: %T", out)
	}
	if payload["status"] != "harvesting" {
		t.Fatalf("回执应标记 harvesting，实际: %+v", payload)
	}
	// 软提示必须存在（用户面对永远转圈的按钮是本工具重构要消灭的体验）
	notes, _ := payload["notes"].(string)
	if !strings.Contains(notes, "自动") || !strings.Contains(notes, "其它") {
		t.Fatalf("回执软提示应告知用户可先做其它事，实际: %s", notes)
	}

	if len(fake.calls) != 1 {
		t.Fatalf("收割器应被启动一次，实际 %d 次", len(fake.calls))
	}
	if len(fake.calls[0].targets) != 2 ||
		fake.calls[0].targets[0].TaskID != "task-a" ||
		fake.calls[0].targets[1].TaskID != "task-b" {
		t.Fatalf("收割目标应按入参对位传递，实际: %+v", fake.calls[0].targets)
	}
}

// TestCollectResults_AlreadyActive 验证去重信号：收割器返回
// ErrHarvestAlreadyActive 时，工具返回「进行中」柔性回执而非错误。
func TestCollectResults_AlreadyActive(t *testing.T) {
	fake := &fakeHarvester{response: ErrHarvestAlreadyActive}
	tool := NewCollectResultsTool(fake)

	out, err := tool.Execute(collectToolContext(), map[string]any{
		"task_ids": []any{"task-a"},
	})
	if err != nil {
		t.Fatalf("去重场景不应报错，实际: %v", err)
	}
	payload, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("应返回回执 map，实际: %T", out)
	}
	if payload["status"] != "harvesting" || payload["reused"] != true {
		t.Fatalf("回执应标记 harvesting/reused，实际: %+v", payload)
	}
}

// TestCollectResults_SessionHintsPairing 验证可选 session_ids 与 task_ids
// 按顺序一一对应传递给收割器（跨重启落盘恢复入口）。
func TestCollectResults_SessionHintsPairing(t *testing.T) {
	fake := &fakeHarvester{}
	tool := NewCollectResultsTool(fake)

	_, err := tool.Execute(collectToolContext(), map[string]any{
		"task_ids":    []any{"task-a", "task-b"},
		"session_ids": []any{"sess-a", "sess-b"},
	})
	if err != nil {
		t.Fatalf("启动收割不应报错，实际: %v", err)
	}
	targets := fake.calls[len(fake.calls)-1].targets
	if targets[0].SessionID != "sess-a" || targets[1].SessionID != "sess-b" {
		t.Fatalf("session 提示应按位对齐，实际: %+v", targets)
	}

	// 数量不匹配 → 引导性错误，不启动收割
	callsBefore := len(fake.calls)
	_, err = tool.Execute(collectToolContext(), map[string]any{
		"task_ids":    []any{"task-a"},
		"session_ids": []any{"sess-a", "sess-b"},
	})
	if err == nil || !strings.Contains(err.Error(), "一一对应") {
		t.Fatalf("数量不匹配应返回一一对应引导，实际: %v", err)
	}
	if len(fake.calls) != callsBefore {
		t.Fatalf("数量不匹配不应启动收割")
	}
}

// TestCollectResults_MissingHarvester 验证收割器未注入时返回引导性错误。
func TestCollectResults_MissingHarvester(t *testing.T) {
	tool := NewCollectResultsTool(nil)
	_, err := tool.Execute(collectToolContext(), map[string]any{
		"task_ids": []any{"task-a"},
	})
	if err == nil || !strings.Contains(err.Error(), "HarvestStarter") {
		t.Fatalf("收割器未注入应返回引导性错误，实际: %v", err)
	}
}
