package tools

import (
	"context"
	"fmt"

	"github.com/DotNetAge/goagent/subagent"
)

// SubAgentTool 是 SubAgent 工具的 goharness 形态（Info/Execute(map) 风格），
// 内部委托注入的 subagent.SubAgentDispatcher 受理派发：
// 同步发出「创建新运行实例的请求」，立即返回受理回执（含跟踪句柄 task_id），
// 子任务在宿主侧后台独立运行；结果由 CollectResults 按跟踪句柄收集。
type SubAgentTool struct {
	dispatcher subagent.SubAgentDispatcher
}

// NewSubAgentTool 创建绑定到指定 Dispatcher 的 SubAgent 工具。
func NewSubAgentTool(dispatcher subagent.SubAgentDispatcher) *SubAgentTool {
	return &SubAgentTool{dispatcher: dispatcher}
}

// Info 返回 SubAgent 工具的元信息。
func (t *SubAgentTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "SubAgent",
		Description: "派发子任务给具名子代理异步执行，立即返回受理回执（task_id / session_id / title），结果落定后自动送达本会话。",
		Prompt: `派发子任务给具名子代理在后台独立执行，立即返回受理回执（跟踪句柄 task_id、子代理会话 session_id、任务标题 title），结果不随本次调用返回——落定后自动送达本会话。

- 并行派发：需要多个子任务时在同一响应中全部派发，然后调用 CollectResults(task_ids...) 启动结果收割。
- 任务描述自包含：子代理看不到你的对话上下文。
- 复用对话：传入 session_id（或省略后系统自动延续该子代理最近的空闲会话）可延续与该子代理的既有对话；被占用时自动新建独立会话。`,
		Tags:    []string{"orchestration", "subagent", "sub-agent"},
		IsAsync: true,
		Parameters: []Parameter{
			{Name: "agent_name", Type: "string", Description: "声明系统内的具名智能体来执行任务，如果要创建分身就用你自己的名称", Required: true},
			{Name: "task", Type: "string", Description: "子代理的任务描述，无需声明子代理的角色，子代理自有其角色定义，直接说明任务内容。", Required: true},
			{Name: "session_id", Type: "string", Description: "要复用的子代理会话 ID。传入后将延续该会话之前的对话上下文；不传时系统自动延续该子代理最近一次的空闲会话（主从对话连贯），被占用时新建独立会话。", Required: false},
		},
	}
}

// Execute 受理子任务派发：解析参数 → Dispatcher.Submit → 回执转为给模型的文本反馈。
// 受理成功返回 status=running 与跟踪句柄 task_id；策略性拒绝（如 agent 不存在）
// 与受理失败均以柔性错误返回（错误原因 + 下一步引导），写入主会话供 LLM 自查纠正。
func (t *SubAgentTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	rawAgentName, ok := GetParam(params, "agent_name")
	agentName := ""
	if ok {
		agentName, _ = rawAgentName.(string)
	}
	if agentName == "" {
		return nil, fmt.Errorf("%s", GuideMissingParam("SubAgent", "agent_name"))
	}
	rawTask, ok := GetParam(params, "task")
	task := ""
	if ok {
		task, _ = rawTask.(string)
	}
	if task == "" {
		return nil, fmt.Errorf("%s", GuideMissingParam("SubAgent", "task"))
	}

	// 可选参数：session_id —— 显式复用已有子代理会话（延续对话上下文）。
	rawSessionID, ok := GetParam(params, "session_id")
	reqSessionID := ""
	if ok {
		reqSessionID, _ = rawSessionID.(string)
	}

	if t.dispatcher == nil {
		return nil, fmt.Errorf("%s", GuideMissingContext("SubAgent", "SubAgentDispatcher（子任务受理器）"))
	}

	receipt, err := t.dispatcher.Submit(ctx, subagent.SubAgentRequest{
		AgentName: agentName,
		Task:      task,
		SessionID: reqSessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide("派发子任务时失败", WithErrDetail("子任务受理流程失败", err), "先自查：我传入的子代理名称是否符合命名要求？若配置无误仍失败，应告知用户检查会话持久化配置"), err)
	}
	if !receipt.Accepted {
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("为任务派发子代理 %q 失败", agentName),
			receipt.Reason,
			"按拒绝原因纠正后重新调用 SubAgent",
		))
	}

	// 回执文本只保留 LLM 需要的最低限度：任务已在后台运行、结果稍后自动
	// 送达、调用 CollectResults 启动收割。长篇机制说明（会话复用规则、
	// 收割器内部行为）是每轮都进历史的噪声，不写入回执。
	notes := "任务已在后台执行，结果稍后自动送达。请调用 CollectResults 启动结果收割（立即返回），并在回复中告知用户可先继续其它工作。"
	return map[string]any{
		"status":     "running",
		"agent_name": agentName,
		"task_id":    receipt.TaskID,
		"session_id": receipt.SessionID,
		"title":      receipt.Title,
		"notes":      notes,
	}, nil
}
