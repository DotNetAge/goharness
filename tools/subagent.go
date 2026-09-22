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
		Description: "派发子任务给另一个 Agent 异步执行，立即返回受理回执（含跟踪句柄 task_id）。之后用 CollectResults 等待并收集结果。",
		Prompt: `为一次性委派任务派发一个子代理。调用后立即返回受理回执（status=running，含跟踪句柄 task_id），子任务在后台独立运行。
关键约束：此工具是异步的，但回合不能就此结束。在同一响应中并行派发全部所需的 SubAgent 后，你必须立即调用 CollectResults(task_ids...) 传入全部跟踪句柄，阻塞等待所有子任务落定并拿到结果后，再基于结果继续作答。严禁在子任务结果收集之前结束回合回答用户。
同一响应中的多个 SubAgent 调用会并行执行。请根据角色命名代理（例如 "code_reviewer"）。任务描述应自包含——子代理无法看到你的对话上下文。`,
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

	// 会话来源软性说明：主 Agent 与子 Agent 的对话是连贯的——
	// 未显式传 session_id 时，系统自动延续该子代理最近一次的空闲会话
	// （被占用才新建）。明确告知 LLM「跟踪句柄已受理、去收集结果」。
	var notes string
	if reqSessionID != "" {
		notes = "已延续与 " + agentName + " 的既有对话。请用 CollectResults 等待跟踪句柄 " + receipt.TaskID + " 的结果。"
	} else {
		notes = "主从 Agent 对话连贯：系统已自动延续与 " + agentName + " 的对话上下文（优先复用其最近空闲会话，被占用则新建）。请用 CollectResults 等待跟踪句柄 " + receipt.TaskID + " 的结果。"
	}
	return map[string]any{
		"status":     "running",
		"agent_name": agentName,
		"task_id":    receipt.TaskID,
		"notes":      notes,
	}, nil
}
