package tools

import (
	"context"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
)

// TestSubAgentTool_ExecutePanicRecovery 验证 SubAgent 工具后台 goroutine 的 panic 兜底：
// spawn 链路发生 panic 时，后台 goroutine 应捕获异常正常退出（进程保护兜底），
// 不得击穿崩溃整个进程；同步阶段仍正常返回 running 存根。
// 注：spawn 内部（agents 层）另有 panic 捕获并登记失败广播，此处仅验证
// tools 层兜底 recover 的进程保护语义。
func TestSubAgentTool_ExecutePanicRecovery(t *testing.T) {
	tool := NewSubAgentTool(func(context.Context, string, string, string) (string, string, error) {
		panic("子代理执行链路异常")
	})
	ctx := WithToolContext(context.Background(), &ToolContext{
		Logger:    logging.NewNopLogger(),
		EmitEvent: func(e events.ReactEvent) {},
	})

	res, err := tool.Execute(ctx, map[string]any{
		"agent_name": "sub-agent",
		"task":       "任务",
	})
	if err != nil {
		t.Fatalf("同步阶段应正常返回 running 存根，实际报错: %v", err)
	}
	result, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("Execute 应返回 map[string]any，实际: %T", res)
	}
	if result["status"] != "running" {
		t.Fatalf("status 应为 running，实际: %v", result["status"])
	}

	// 等待后台 goroutine 执行完毕：panic 被兜底 recover 捕获后 goroutine 正常退出，
	// 测试进程不崩溃即为本用例的通过标准（若兜底缺失，panic 会击穿导致进程崩溃）。
	// goroutine 无完成事件可等（panic 场景不发 SubtaskCompleted），用短暂等待务实处理。
	time.Sleep(200 * time.Millisecond)
}
