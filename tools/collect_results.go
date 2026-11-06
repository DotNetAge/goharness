package tools

import (
	"context"
	"errors"
	"fmt"
)

// ErrHarvestAlreadyActive 表示收割目标全部已在登记中（去重信号）：
// 收割启动器对「无新增目标」的重复调用返回此错误，工具层将其转为柔性
// 回执而非失败；有新增目标的调用会被按 task_id 合并登记并返回成功。
var ErrHarvestAlreadyActive = errors.New("harvest already active")

// HarvestTarget 是一个收割目标：轮内跟踪句柄 + 可选的子代理会话 ID 提示。
// SessionID 是长期锚点（SubAgent 受理回执返回，LLM 上下文可见），仅在
// 跨重启恢复（进程内句柄登记已丢失）时作为落盘事实查询的入口。
// AgentName / Title 是展示信息：LLM 调用方不传（零值），收割启动器从
// 受理登记表补全后随意图落盘，回灌报告据此呈现可读条目。
type HarvestTarget struct {
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	Title     string `json:"title,omitempty"`
}

// HarvestStatus 是单个收割目标的终态条目。
// Status 取值：completed（成功，Result 为最终答案）/ failed（失败，Error 为
// 原因）/ blocked（超限仍未落定且在等待用户回答或授权）/ timeout（超限仍未
// 落定，可能仍在执行或已挂死）。
type HarvestStatus struct {
	TaskID    string `json:"task_id"`
	SessionID string `json:"session_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status"`
	Result    string `json:"result,omitempty"`
	Error     string `json:"error,omitempty"`
}

// HarvestReport 是收割汇报：全部目标落定或达到总上限时产生一次，
// 经宿主注入的收割轮询器回灌主会话（应用侧 WithHarvestIntentRecorder 接缝）。
type HarvestReport struct {
	MainSessionID string          `json:"main_session_id"`
	Targets       []HarvestStatus `json:"targets"`
}

// HarvestStarter 是后台收割启动器（agents 层 subAgentManager 实现），
// 由工具注册时注入 CollectResults。实现必须非阻塞：只登记收割意图并
// 立即返回；落定判定与结果回灌由应用侧轮询器执行。
// 全部目标均已登记（无新增）时返回 ErrHarvestAlreadyActive；有新增目标的
// 重复调用会被按 task_id 合并登记（收割在飞不丢任务）。
type HarvestStarter interface {
	StartHarvest(mainSessionID string, targets []HarvestTarget) error
}

// CollectResultsTool 收集子代理任务的结果（非阻塞收割启动器）。
//
// 旧设计（阻塞 select rt.Done()）以卡死方式死等结果，主会话串行队列被
// 永久占住——子任务挂死即大梁无法对话。新设计：调用即启动后台收割并
// 立即返回软提示回执；收割器周期轮询子会话落盘事实，全部落定或超限后
// 把结果回灌主会话（回灌通道由宿主注入）。本工具壳只做「启动 + 回执」，
// 不等待任何子任务。
type CollectResultsTool struct {
	harvester HarvestStarter
}

// NewCollectResultsTool 创建绑定到指定收割器的 CollectResults 工具。
func NewCollectResultsTool(harvester HarvestStarter) *CollectResultsTool {
	return &CollectResultsTool{harvester: harvester}
}

func (t *CollectResultsTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "CollectResults",
		MaxResultSizeChars: 50000,
		Description:        "启动子任务结果收割（立即返回），全部任务落定后结果自动送达本会话",
		Prompt: `启动子任务结果收割，调用后立即返回（status=harvesting）；全部任务落定后结果自动送达本会话，无需再次调用等待。

- task_ids：SubAgent 受理回执返回的跟踪句柄。
- session_ids（可选）：与 task_ids 一一对应的子代理会话 ID，用于跨重启恢复结果；不需要可整体省略。
- 收割进行中又有新派发时请再次调用（新任务自动合并进收割）；仅重复传入已登记任务时返回进行中回执，属正常现象。`,
		Tags:         []string{"orchestration", "collect", "result"},
		IsIdempotent: true,
		Parameters: []Parameter{
			{Name: "task_ids", Type: "array", Description: "要收割结果的子任务跟踪句柄数组（来自 SubAgent 受理回执）。", Required: true},
			{Name: "session_ids", Type: "array", Description: "可选：与 task_ids 一一对应的子代理会话 ID 数组，用于跨重启恢复结果。", Required: false},
		},
	}
}

// Execute 启动后台收割并立即返回软提示回执（不等待任何子任务）。
// 收割结果由收割器在全部落定/超限后经宿主汇报回调回灌主会话；
// 回执中的软提示要求 LLM 在回复中告知用户「结果自动送达，可先继续其它
// 工作」——不写这句用户面对一个永远转圈的按钮，完全不知道后台在发生什么。
func (t *CollectResultsTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	logger := getLogger(ctx)

	rawIDsVal, found := GetParam(params, "task_ids")
	if !found {
		return nil, fmt.Errorf("%s", GuideMissingParam("CollectResults", "task_ids"))
	}
	rawIDs, ok := rawIDsVal.([]any)
	if !ok {
		return nil, fmt.Errorf("%s", GuideWrongParamType("CollectResults", "task_ids", "array", rawIDsVal))
	}
	taskIDs := make([]string, 0, len(rawIDs))
	for _, raw := range rawIDs {
		if id, ok := raw.(string); ok && id != "" {
			taskIDs = append(taskIDs, id)
		}
	}
	if len(taskIDs) == 0 {
		return nil, fmt.Errorf("%s", GuideInvalidValue("CollectResults", "task_ids", rawIDsVal, "传入 SubAgent 受理回执返回的跟踪句柄（task_id 字符串数组），至少一个"))
	}

	// 可选的会话 ID 提示：与 task_ids 按原始数组位置一一对应（空串占位
	// 合法，等价于该任务无提示）。按原始数组长度校验对位关系——先过滤
	// 空串再比数量会把含空占位的合法对位输入误判为参数错误（第三轮审计），
	// 且空占位过滤后错位会张冠李戴。
	rawHints := make([]string, 0, len(rawIDs))
	if rawHintsVal, found := GetParam(params, "session_ids"); found {
		rawHintsAny, ok := rawHintsVal.([]any)
		if !ok {
			return nil, fmt.Errorf("%s", GuideWrongParamType("CollectResults", "session_ids", "array", rawHintsVal))
		}
		if len(rawHintsAny) != len(rawIDs) {
			return nil, fmt.Errorf("%s", GuideInvalidValue("CollectResults", "session_ids", rawHintsVal,
				"session_ids 与 task_ids 必须一一对应（数量一致、顺序相同）；不需要会话恢复时可整体省略"))
		}
		for _, raw := range rawHintsAny {
			if id, ok := raw.(string); ok {
				rawHints = append(rawHints, id)
			} else {
				rawHints = append(rawHints, "")
			}
		}
	}

	if t.harvester == nil {
		return nil, fmt.Errorf("%s", GuideMissingContext("CollectResults", "HarvestStarter（后台收割器）"))
	}
	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil {
		return nil, fmt.Errorf("%s", GuideMissingContext("CollectResults", "会话上下文"))
	}

	// task_ids 与 session_ids 同一游标配对构建：空 task_id 占位两侧一起跳过，
	// 保证提示与任务不错位。
	type taskHint struct {
		taskID    string
		sessionID string
	}
	pairs := make([]taskHint, 0, len(rawIDs))
	for i, raw := range rawIDs {
		id, ok := raw.(string)
		if !ok || id == "" {
			continue
		}
		hint := ""
		if i < len(rawHints) {
			hint = rawHints[i]
		}
		pairs = append(pairs, taskHint{taskID: id, sessionID: hint})
	}

	targets := make([]HarvestTarget, len(pairs))
	for i, p := range pairs {
		targets[i] = HarvestTarget{TaskID: p.taskID, SessionID: p.sessionID}
	}

	if err := t.harvester.StartHarvest(tc.Session.ID(), targets); err != nil {
		if errors.Is(err, ErrHarvestAlreadyActive) {
			// 去重：全部目标均已登记（无新增），重复调用不报错、不重复收割。
			return map[string]any{
				"status": "harvesting",
				"count":  len(taskIDs),
				"reused": true,
				"notes":  "这些任务的收割均已在进行中，全部落定后结果自动汇报到本会话。",
			}, nil
		}
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide("启动后台收割时失败", WithErrDetail("收割器启动失败", err), "稍后重新调用 CollectResults；若持续失败请告知用户检查 daemon 状态"), err)
	}

	logger.Info("collect_results: background harvest started",
		"main_session", tc.Session.ID(),
		"task_ids", taskIDs,
		"count", len(taskIDs),
	)
	return map[string]any{
		"status": "harvesting",
		"count":  len(taskIDs),
		"notes":  "后台收割已登记，各任务落定后结果自动汇报到本会话。请在回复中告知用户：结果稍后自动送达，可先继续其它工作或对话。",
	}, nil
}
