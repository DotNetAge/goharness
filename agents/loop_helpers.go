package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/session"
	"github.com/DotNetAge/goharness/tools"
)

// loop_helpers.go 聚合换核后仍被新执行链（runWithGoAgent / 工具适配器 /
// 授权挂起恢复 / 压缩请求构造）引用的循环辅助函数。
// 旧核 executor.go 的循环骨架与专属辅助已随换核拆除，本文件承接其中
// 与引擎无关、纯粹服务于循环语义的部分。

// llmCallTimeout 决定单次 LLM 调用的超时预算，优先级从高到低：
//  1. 模型配置的 RequestTimeout（秒）：按模型显式配置，适配慢模型（思考/首 token 耗时远超默认值）；
//  2. 否则跟随 ctx 截止时间的剩余时长（调用方设置的整体预算）；
//  3. 否则回退默认 defaultLLMTimeout（4 分钟）。
func llmCallTimeout(requestTimeoutSec int64, ctx context.Context) time.Duration {
	if requestTimeoutSec > 0 {
		return time.Duration(requestTimeoutSec) * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining > 0 {
			return remaining
		}
	}
	return defaultLLMTimeout
}

// formatToolResult 将 ToolResult 格式化为要持久化到会话的字符串。
// 正常执行路径（工具适配器）与授权挂起恢复路径（executePendingAndAppend）
// 共用本函数，保证错误前缀、空结果引导等文案完全一致。
func formatToolResult(tr hooks.ToolResult) string {
	if tr.Error != "" {
		return fmt.Sprintf("[%s] 执行错误: %s", tr.ToolName, tr.Error)
	}
	if tr.Result != "" {
		return tr.Result
	}
	// 空结果属于"不及预期"场景，同样采用第一人称引导，提示调整参数或换工具。
	return fmt.Sprintf("[%s] 返回结果: (空结果)。我未能从该工具获得任何输出，下一步我应该考虑调整参数或改用其它工具来获取所需信息。", tr.ToolName)
}

// multiAgentToolNames 多 Agent 协作类工具清单：任务派发（SubAgent/TeamCreate）、
// 结果收集（CollectResults，与 SubAgent 配对使用）、团队管理（TeamDelete/TeamList/
// TeamGetTasks）。子智能体会话必须全部屏蔽（见 effectiveExcludeTools 的 Sponsor 判定），
// 防止「子派孙」递归派发；与 registerDefaultTools 的注册分支保持同集，
// 新增多 Agent 工具时须两处同步维护。
var multiAgentToolNames = []string{
	"SubAgent",
	"CollectResults",
	"TeamCreate",
	"TeamDelete",
	"TeamList",
	"TeamGetTasks",
}

// effectiveExcludeTools 汇总执行循环实际生效的工具排除集合：
// Agent 声明的 ExcludeTools + 子会话的多 Agent 协作工具屏蔽。
//
// 子智能体会话（spawn 派生，Sponsor 非空）必须屏蔽全部多 Agent 协作工具。
// 根因：子会话复用主 Agent 的系统提示与完整工具集，当任务与角色错配
// （如给执行助理派翻译任务）或单批工作量偏大时，模型会按系统提示中
// 「协调其它智能体」的职责再次调用 SubAgent/TeamCreate 派发，形成
// 「子派孙」递归——每层 spawn 同步阻塞等待下一层，CollectResults 永远
// 等不到终止标记，表现为所有子任务永久处于运行中（假死循环）。
func effectiveExcludeTools(rt *Runtime, agentName string, sess *session.Session) map[string]bool {
	exclude := rt.ExcludeToolsFor(agentName)
	if sess != nil && sess.Sponsor() != "" {
		for _, name := range multiAgentToolNames {
			exclude[name] = true
		}
	}
	return exclude
}

// buildAllToolDefinitions 从工具注册表构建工具定义，用于 LLM 请求的 tools 字段。
// 所有工具一次性注册，不在迭代间改变工具集，以保持前缀缓存稳定。
// exclude 为非空时，被排除的工具不会出现在 tools 字段中，LLM 将无法调用它们。
// 主对话请求（工具适配器）与压缩请求（compactor）共用本函数。
func buildAllToolDefinitions(registry tools.ToolRegistry, exclude map[string]bool) []gochatcore.Tool {
	allTools := registry.All()
	if len(allTools) == 0 {
		return nil
	}
	out := make([]gochatcore.Tool, 0, len(allTools))
	for _, t := range allTools {
		info := t.Info()
		if exclude != nil && exclude[info.Name] {
			continue
		}
		var paramsJSON json.RawMessage
		if info.RawSchema != nil {
			// MCP / 外部工具：直接透传完整 JSON Schema（保留嵌套 / 联合类型 / $ref 等）
			b, err := json.Marshal(info.RawSchema)
			if err != nil {
				paramsJSON = buildParamSchema(info.Parameters)
			} else {
				paramsJSON = b
			}
		} else {
			paramsJSON = buildParamSchema(info.Parameters)
		}
		out = append(out, gochatcore.Tool{
			Name:        info.Name,
			Description: info.Description,
			Parameters:  paramsJSON,
		})
	}
	return out
}

// dupErrorTracker 跟踪同一工具连续出现的完全相同错误，达到阈值时生成引导话术，
// 提示大模型更换工作方式（而非硬性熔断循环）。
//
// 计数规则：
//   - 工具返回错误且错误文本未自带「下一步我应该」引导时，按工具名累计连续相同错误次数；
//     错误文本变化则重新从 1 开始计数。
//   - 工具成功执行、或错误已含引导时，重置该工具的全部计数状态，
//     确保后续若再出现错误从第 1 次开始计数。
//   - 引导话术对每个工具仅注入一次（guided 标记），避免每轮重复注入同一段话术。
//
// 注入的话术追加到 tool 消息末尾，不影响 system prompt 与 tools 字段构成的前缀缓存。
type dupErrorTracker struct {
	dupCount map[string]int
	lastErr  map[string]string
	guided   map[string]bool
}

func newDupErrorTracker() dupErrorTracker {
	return dupErrorTracker{
		dupCount: make(map[string]int),
		lastErr:  make(map[string]string),
		guided:   make(map[string]bool),
	}
}

// maybeGuide 根据工具结果决定是否生成重复错误引导话术。
// 返回 guide 为应追加到工具结果末尾的话术（空串表示不追加），count 为该工具当前的连续相同错误次数。
// map 为引用类型，故值接收器即可在调用间保持并修改状态。
func (t dupErrorTracker) maybeGuide(tr hooks.ToolResult) (guide string, count int) {
	// 工具成功执行或错误已含引导时，重置该工具的重复错误跟踪状态。
	if tr.Error == "" || strings.Contains(tr.Error, "下一步我应该") {
		delete(t.dupCount, tr.ToolName)
		delete(t.lastErr, tr.ToolName)
		delete(t.guided, tr.ToolName)
		return "", 0
	}
	if t.lastErr[tr.ToolName] == tr.Error {
		t.dupCount[tr.ToolName]++
	} else {
		t.lastErr[tr.ToolName] = tr.Error
		t.dupCount[tr.ToolName] = 1
	}
	count = t.dupCount[tr.ToolName]
	if count >= duplicateErrorThreshold && !t.guided[tr.ToolName] {
		t.guided[tr.ToolName] = true
		guide = fmt.Sprintf(duplicateErrorGuideTemplate, count, tr.ToolName)
	}
	return guide, count
}
