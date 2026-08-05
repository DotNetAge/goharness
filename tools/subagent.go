package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/DotNetAge/goharness/events"
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
//   - sessionID: 要复用的子代理会话 ID。非空时延续该会话上下文，为空时新建会话（分身）。
//
// 返回：
//   - string: 子代理的执行结果
//   - string: 子代理的会话 ID（用于持久化加载）
//   - error: 执行过程中的错误
type SpawnFunc func(ctx context.Context, agentName, task, sessionID string) (result string, sid string, err error)

// EnsureSubAgentSessionFunc 用于同步获取或创建子代理 session 的 ID。
// 在 SubAgent.Execute() 的 goroutine 之前调用，确保 session_id 可同步返回。
// sessionID 为调用方显式传入的复用目标；非空时返回该会话 ID，为空时创建新会话。
type EnsureSubAgentSessionFunc func(ctx context.Context, agentName, sessionID string) (string, error)

// SubAgentTool 实现了子代理调度工具。
// 允许 LLM 生成子代理来处理委派的任务。
//
// 特性：
//   - 异步执行：立即返回 {status: "running", agent_name, session_id}
//   - 结果收集：通过 CollectResultsTool 获取结果（轮询子 session）
//   - 并发控制：最多 20 个并发子代理
//   - 事件通知：发送 SubtaskSpawned 和 SubtaskCompleted 事件
//
// 适用场景：
//   - 专业任务委托（如代码审查、数据分析）
//   - 任务并行化（将大任务拆分为独立子任务）
type SubAgentTool struct {
	spawn         SpawnFunc
	ensureSession EnsureSubAgentSessionFunc // 同步获取子 session ID
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

// SetEnsureSessionFunc 设置获取/创建子 session ID 的函数。
// 在 goroutine 前同步调用，返回 session_id 作为存根。
func (t *SubAgentTool) SetEnsureSessionFunc(fn EnsureSubAgentSessionFunc) {
	t.ensureSession = fn
}

// Info 返回 SubAgent 工具的元信息。
func (t *SubAgentTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:        "SubAgent",
		Description: "为任务生成一个子代理。之后可使用 CollectResults 获取结果。",
		Prompt: `为一次性委派任务生成一个子代理。立即返回 {status: "running", agent_name, session_id}。

关键约束：此工具是异步的。你不会在同一轮中看到结果。请使用 CollectResults(session_ids) 稍后获取结果。

同一响应中的多个 SubAgent 调用会并行执行。请根据角色命名代理（例如 "code_reviewer"）。任务描述应自包含——子代理无法看到你的对话上下文。`,
		Tags:    []string{"orchestration", "subagent", "sub-agent"},
		IsAsync: true,
		Parameters: []Parameter{
			{Name: "agent_name", Type: "string", Description: "要生成的子代理名称。", Required: true},
			{Name: "task", Type: "string", Description: "子代理的任务描述。", Required: true},
			{Name: "session_id", Type: "string", Description: "要复用的子代理会话 ID。传入后将延续该会话之前的对话上下文；不传时系统自动延续该子代理最近一次的空闲会话（主从对话连贯），被占用时新建独立会话。", Required: false},
		},
	}
}

// Execute 执行子代理创建操作。
//
// 处理流程：
//  1. 验证必需参数（agent_name, task）
//  2. 检查 ToolContext 是否包含必要组件
//  3. 同步获取子 session ID（存根）
//  4. 发送 SubtaskSpawned 事件
//  5. 在后台 goroutine 中运行子代理
//  6. 结果自动写入子 session，CollectResults 轮询获取
//  7. 发送 SubtaskCompleted 事件
//
// 参数：
//   - ctx: 上下文（必须包含 ToolContext）
//   - params: 必须包含 "agent_name" 和 "task"
//
// 返回：
//   - map[string]any: 包含 status, agent_name, session_id, notes（session_id 用法说明）
//   - error: 参数缺失或配置不完整时返回错误
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
	// 不传时新建独立会话（分身），传时由 ensureSession/spawn 复用该会话。
	rawSessionID, ok := GetParam(params, "session_id")
	reqSessionID := ""
	if ok {
		reqSessionID, _ = rawSessionID.(string)
	}

	logger := getLogger(ctx)

	tc := GetToolContext(ctx)
	if tc == nil || tc.EmitEvent == nil {
		return nil, fmt.Errorf("%s", GuideMissingContext("SubAgent", "包含 EventBus 的 ToolContext"))
	}
	if t.spawn == nil {
		return nil, fmt.Errorf("%s", GuideMissingContext("SubAgent", "SpawnFunc（子代理创建函数）"))
	}

	logger.Info("spawning sub-agent",
		"agent_name", agentName,
		"task", truncateForLog(task, 100),
	)

	// === 同步阶段：获取子 session ID（存根）===
	// 会话由 SessionID 唯一定位（1 ProjectDir → N Session 分身模型）：
	// 不传 session_id 时新建会话，传时复用该会话。
	// 在 goroutine 前调用，确保 session_id 可同步返回给 spawn 结果。
	sessionID := reqSessionID
	if t.ensureSession != nil {
		sid, err := t.ensureSession(ctx, agentName, reqSessionID)
		if err != nil {
			return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide("获取子代理 session 时失败", WithErrDetail("子代理 session 的创建或持久化失败", err), "先自查：我传入的子代理名称是否符合命名要求？若配置无误仍失败，应告知用户检查会话持久化配置"), err)
		}
		sessionID = sid
		logger.Info("sub-agent session ensured",
			"agent_name", agentName,
			"session_id", sessionID,
		)
	}

	tc.EmitEvent(events.ReactEvent{
		AgentName: agentName,
		Type:      events.SubtaskSpawned,
		Data: events.SubtaskInfo{
			AgentName:   agentName,
			Description: task,
			SessionID:   sessionID,
		},
	})

	// 在后台运行子代理
	subagentSem <- struct{}{}
	go func() {
		startedAt := time.Now()
		defer func() { <-subagentSem }()

		result, _, err := t.spawn(ctx, agentName, task, sessionID)

		completedAt := time.Now()

		if err != nil {
			logger.Error("sub-agent task failed", err,
				"agent_name", agentName,
				"session_id", sessionID,
				"elapsed_ms", completedAt.Sub(startedAt).Milliseconds(),
			)
		} else {
			logger.Info("sub-agent task completed",
				"agent_name", agentName,
				"session_id", sessionID,
				"elapsed_ms", completedAt.Sub(startedAt).Milliseconds(),
				"result_len", len(result),
			)
		}

		// 结果已自动写入子 session（由 runtime 持久化）
		// CollectResults 通过轮询子 session 获取结果

		var (
			resultAnswer string
			resultError  string
		)
		if err != nil {
			resultError = err.Error()
		} else {
			resultAnswer = result
		}
		tc.EmitEvent(events.ReactEvent{
			AgentName: agentName,
			Type:      events.SubtaskCompleted,
			Data: events.SubtaskResult{
				AgentName:   agentName,
				Success:     err == nil,
				Answer:      resultAnswer,
				Error:       resultError,
				Description: task,
				SessionID:   sessionID,
			},
		})
	}()

	// 会话来源软性说明：主 Agent 与子 Agent 的对话是连贯的——
	// 未显式传 session_id 时，系统自动延续该子代理最近一次的空闲会话
	// （被占用才新建）。明确告知 LLM「返回相同 session_id = 对话在延续」，
	// 它无需为每次任务新建会话。
	var notes string
	if reqSessionID != "" {
		notes = "已延续与 " + agentName + " 的既有对话（会话 " + sessionID + "）。"
	} else {
		notes = "主从 Agent 对话连贯：本次已自动延续与 " + agentName + " 的对话上下文（会话 " + sessionID + "，优先复用其最近空闲会话，被占用则新建）。"
	}
	return map[string]any{
		"status":     "running",
		"agent_name": agentName,
		"session_id": sessionID,
		"notes":      notes,
	}, nil
}
