package tools

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/store"
)

// subagentSem 是子代理的并发信号量。
// 限制同时运行的子代理数量，防止资源耗尽。
var subagentSem = make(chan struct{}, 20)

// SpawnFunc 是子代理创建和运行函数类型。
// 由 reactor 提供，避免循环依赖。
//
// 参数：
//   - ctx: 上下文
//   - agentName: 子代理名称/角色
//   - task: 任务描述
//
// 返回：
//   - string: 子代理的执行结果
//   - error: 执行过程中的错误
type SpawnFunc func(ctx context.Context, agentName, task string) (string, error)

// SubAgentTool 实现了子代理调度工具。
// 允许 LLM 生成子代理来处理委派的任务。
//
// 特性：
//   - 异步执行：立即返回 {task_id, status: "running"}
//   - 结果收集：通过 CollectResultsTool 获取结果（从 ResultStore 读取）
//   - 并发控制：最多 20 个并发子代理
//   - 事件通知：发送 SubtaskSpawned 和 SubtaskCompleted 事件
//
// 适用场景：
//   - 专业任务委托（如代码审查、数据分析）
//   - 任务并行化（将大任务拆分为独立子任务）
type SubAgentTool struct {
	spawn   SpawnFunc    // 子代理创建函数
	counter atomic.Int64 // 任务 ID 计数器
}

// NewSubAgentTool 创建一个 SubAgentTool 实例。
//
// 参数：
//   - spawn: 子代理创建函数（由 reactor 注入）
//
// 返回：
//   - *SubAgentTool: 配置好的 SubAgentTool 实例
func NewSubAgentTool(spawn SpawnFunc) *SubAgentTool {
	return &SubAgentTool{spawn: spawn}
}

// Info 返回 SubAgent 工具的元信息。
func (t *SubAgentTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "SubAgent",
		Description: "Spawn a sub-agent for a task. Use CollectResults to retrieve the result later.",
		Prompt: `Spawn a sub-agent for a one-shot delegated task. Returns immediately with {task_id, status: "running"}.

Key constraint: This tool is ASYNC. You will NOT see the result in the same round. Use CollectResults(task_ids) to retrieve results later.

Multiple SubAgent calls in the same response run in parallel. Name the agent based on its role (e.g. "code_reviewer"). The task description should be self-contained — the sub-agent does not see your conversation context.`,
		Tags:    []string{"orchestration", "subagent", "sub-agent"},
		IsAsync: true,
		Parameters: []Parameter{
			{Name: "agent_name", Type: "string", Description: "Name of the sub-agent to spawn.", Required: true},
			{Name: "task", Type: "string", Description: "Task description for the sub-agent.", Required: true},
		},
	}
}

// Execute 执行子代理创建操作。
//
// 处理流程：
//  1. 验证必需参数（agent_name, task）
//  2. 检查 ToolContext 是否包含必要组件
//  3. 生成唯一任务 ID
//  4. 发送 SubtaskSpawned 事件
//  5. 在后台 goroutine 中运行子代理
//  6. 将结果存储到 ResultStore
//  7. 发送 SubtaskCompleted 事件
//
// 参数：
//   - ctx: 上下文（必须包含 ToolContext）
//   - params: 必须包含 "agent_name" 和 "task"
//
// 返回：
//   - map[string]any: 包含 task_id, status, agent_name
//   - error: 参数缺失或配置不完整时返回错误
func (t *SubAgentTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	agentName, _ := params["agent_name"].(string)
	if agentName == "" {
		return nil, fmt.Errorf("agent_name is required")
	}
	task, _ := params["task"].(string)
	if task == "" {
		return nil, fmt.Errorf("task is required")
	}

	logger := getLogger(ctx)

	tc := GetToolContext(ctx)
	if tc == nil || tc.EmitEvent == nil {
		return nil, fmt.Errorf("subagent tool requires ToolContext with EventBus")
	}
	if t.spawn == nil {
		return nil, fmt.Errorf("subagent tool: SpawnFunc not configured")
	}

	logger.Info("spawning sub-agent",
		"agent_name", agentName,
		"task", truncateForLog(task, 100),
	)

	taskID := fmt.Sprintf("subagent-%d", t.counter.Add(1))

	tc.EmitEvent(events.ReactEvent{
		Type: events.SubtaskSpawned,
		Data: events.SubtaskInfo{
			TaskID:      taskID,
			AgentName:   agentName,
			Description: task,
		},
	})

	// Run sub-agent in background
	subagentSem <- struct{}{}
	go func() {
		startedAt := time.Now()
		defer func() { <-subagentSem }()

		result, err := t.spawn(ctx, agentName, task)
		completedAt := time.Now()

		if err != nil {
			logger.Error("sub-agent task failed", err,
				"agent_name", agentName,
				"task_id", taskID,
				"elapsed_ms", completedAt.Sub(startedAt).Milliseconds(),
			)
		} else {
			logger.Info("sub-agent task completed",
				"agent_name", agentName,
				"task_id", taskID,
				"elapsed_ms", completedAt.Sub(startedAt).Milliseconds(),
				"result_len", len(result),
			)
		}

		var taskResult *store.TaskResult
		if err != nil {
			taskResult = &store.TaskResult{TaskID: taskID, Error: err.Error(), Done: true}
		} else {
			taskResult = &store.TaskResult{TaskID: taskID, Result: result, Done: true}
		}
		if tc.ResultStore != nil {
			tc.ResultStore.Store(taskID, taskResult)
		}
		tc.EmitEvent(events.ReactEvent{
			AgentID: agentName,
			Type:    events.SubtaskCompleted,
			Data: events.SubtaskResult{
				TaskID:  taskID,
				Success: err == nil,
			},
		})
	}()

	return map[string]any{
		"task_id":    taskID,
		"status":     "running",
		"agent_name": agentName,
	}, nil
}
