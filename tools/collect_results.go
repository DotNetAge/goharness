package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/DotNetAge/goagent"
)

// CollectResultsTool 收集子代理任务的结果。
// 结果不靠轮询子会话，而是直接读取控制平面（goagent.DefaultRuntimeManager）：
// 对每个跟踪句柄定位运行实例，阻塞等待其 Done 关闭（终态结算），
// Completed 读取结果、Failed/Cancelled 读取失败原因。
// 本工具不依赖其它工具、不做会话扫描——跟踪句柄来自 SubAgent 的受理回执。
type CollectResultsTool struct{}

func NewCollectResultsTool() *CollectResultsTool {
	return &CollectResultsTool{}
}

func (t *CollectResultsTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "CollectResults",
		MaxResultSizeChars: 50000,
		Description:        "收集子代理任务的结果（按跟踪句柄阻塞等待任务落定）",
		Prompt: `收集子代理任务的结果。支持重试与恢复。

返回 JSON 数组，每项包含 {task_id, status, result}。

当存在子代理跟踪句柄（SubAgent 回执返回的 task_id）时，请优先调用此工具：
- 首次调用：等待正在运行的任务落定并返回结果
- 重试：从已落定的任务中恢复结果（按句柄直接读取）

如果返回 status=completed，result 字段包含子代理的最终答案。
如果某个 task_id 返回「任务句柄不存在」，说明该任务已无法跟踪
（如服务重启），请对该 agent 重新发起 SubAgent 调用
（task 设为"继续之前的任务并给出最终结果"），再收集新结果。`,
		Tags:         []string{"orchestration", "collect", "result"},
		IsIdempotent: true,
		Parameters: []Parameter{
			{Name: "task_ids", Type: "array", Description: "要收集结果的子任务跟踪句柄数组。task_id 来自 SubAgent 受理回执。", Required: true},
		},
	}
}

// Execute 按跟踪句柄收集子任务结果。
// 并发等待所有句柄落定（单个子任务的长时间执行或挂起不得阻塞其它子任务的
// 结果收集），结果按入参顺序写入预分配槽位，保证返回顺序与 task_ids 一致。
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
	logger.Info("collect_results: collecting results",
		"task_ids", taskIDs,
		"count", len(taskIDs),
	)

	// 仅剥离单次工具执行的超时截止时间，保留父 context 的取消信号：
	// 用户点击停止按钮（message.cancel）必须能中断此处的等待，
	// 否则会话队列会被一直占住，后续消息全部排队、取消形同虚设。
	waitCtx := withoutDeadline(ctx)

	// 并发等待所有子任务落定：单个子任务的长时间执行或挂起不得阻塞其它
	// 子任务的结果收集（顺序等待时，排在前面的在跑任务会让已落定任务的
	// 失败结果迟迟无法上报）。结果按入参顺序写入预分配槽位，
	// 保证返回顺序与 task_ids 一致。
	manager := goagent.DefaultRuntimeManager()
	jsonResults := make([]map[string]string, len(taskIDs))
	var wg sync.WaitGroup
	for i, id := range taskIDs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			jsonResults[i] = t.collectOne(waitCtx, manager, id)
		}(i, id)
	}
	wg.Wait()

	out, err := json.Marshal(jsonResults)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", BuildGuide("序列化收集结果时失败", WithErrDetail("结果数据包含无法序列化的内容（如非法值或循环引用）", err), "检查收集到的子代理结果数据，剔除无法序列化的字段后重试"), err)
	}
	return string(out), nil
}

// collectOne 等待单个跟踪句柄对应的子任务落定并返回结果条目。
// 句柄不存在（daemon 重启后旧句柄、拼写错误等）时立即返回引导性失败，
// 提示 LLM 重新派发，而不是无限等待。
func (t *CollectResultsTool) collectOne(ctx context.Context, manager *goagent.RuntimeManager, taskID string) map[string]string {
	rt, ok := manager.Get(taskID)
	if !ok {
		return map[string]string{
			"task_id": taskID,
			"status":  "failed",
			"error":   "任务句柄不存在，请对该 agent 重新派发 SubAgent 任务后再收集",
		}
	}

	select {
	case <-rt.Done():
	case <-ctx.Done():
		// 用户停止（取消意味着用户已主动停止，父 LLM 不应再尝试续跑）。
		return map[string]string{
			"task_id": taskID,
			"status":  "failed",
			"error":   "已取消：等待子代理结果时被用户中断",
		}
	}

	switch rt.Status() {
	case goagent.StatusCompleted:
		return map[string]string{
			"task_id": taskID,
			"status":  "completed",
			"result":  rt.Result(),
		}
	case goagent.StatusCancelled:
		reason := rt.Reason()
		if reason == "" {
			reason = "子代理任务被取消"
		}
		return map[string]string{
			"task_id": taskID,
			"status":  "failed",
			"error":   "子代理任务失败: " + reason,
		}
	default:
		// Failed 或其余终态：失败原因经 Runtime.Reason() 可查。
		reason := rt.Reason()
		if reason == "" {
			reason = rt.Status().String()
		}
		return map[string]string{
			"task_id": taskID,
			"status":  "failed",
			"error":   "子代理任务失败: " + reason,
		}
	}
}

// withoutDeadline 返回剥离截止时间、但保留取消信号与上下文值的 context。
// 用于让长耗时等待工具（CollectResults）突破单次工具执行超时，
// 同时仍能响应上级取消（如 message.cancel 停止按钮）。
func withoutDeadline(ctx context.Context) context.Context {
	return &noDeadlineContext{Context: ctx}
}

// noDeadlineContext 内嵌父 context，仅重写 Deadline() 使其不再返回截止时间；
// Done()/Err()/Value() 均委托给父 context，取消信号与上下文值依然有效。
type noDeadlineContext struct {
	context.Context
}

func (*noDeadlineContext) Deadline() (time.Time, bool) { return time.Time{}, false }
