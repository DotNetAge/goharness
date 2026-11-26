package loop

import (
	"fmt"
	"sync"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/session"
)

// defaultTaskBoardNudgeEvery 是默认提醒间隔：连续这么多次工具调用没碰任务板才提醒一次。
// 取值权衡：太小会在正常探索期反复打断（每轮都要读一遍提示词，白花 token），
// 太大又追不上「跑着跑着就忘了」的实际节奏。8 次大致对应一次连续读文件+改代码+跑验证。
const defaultTaskBoardNudgeEvery = 8

// TaskBoardNudge 是长循环里的任务板提醒钩子。
//
// 动机（实证）：Agent 开了任务板之后，长任务越跑越容易把回写丢掉——任务一直
// 停在 pending，用户看到的是「完成 0/5」永久静止的假进度。靠系统提示词喊话
// 没有约束力（上下文一长就被挤到注意力边缘），这里改成在请求组装阶段由宿主机
// 兜底注入一次提醒：只在「确实建过任务板」且「确实很久没回写」时才说一句。
//
// 判定完全基于会话历史（assistant 消息里的 ToolCalls），不依赖任务存储，也不
// 影响持久化：提醒只追加到本次请求的 system 段末尾（动态段），不落历史，
// 因此不污染 UI、不破坏 KV 缓存前缀。
type TaskBoardNudge struct {
	// Every 为提醒间隔（次工具调用）；<=0 时取默认值。
	Every int

	mu sync.Mutex
	// nudgedAt 记录每个会话上次提醒时的「未回写工具调用数」，用于同档位不重复提醒。
	nudgedAt map[string]int
}

// NewTaskBoardNudge 创建任务板提醒钩子。every <= 0 时使用默认间隔。
func NewTaskBoardNudge(every int) *TaskBoardNudge {
	if every <= 0 {
		every = defaultTaskBoardNudgeEvery
	}
	return &TaskBoardNudge{Every: every, nudgedAt: map[string]int{}}
}

// Priority 排在收敛钩子（49）之后：收敛判断优先，本钩子只在循环继续时才说话。
func (h *TaskBoardNudge) Priority() int { return 51 }

// BeforeLLM 在请求组装前追加一条任务板提醒（仅在触发条件满足时）。
func (h *TaskBoardNudge) BeforeLLM(sessionID string, iteration int, in *hooks.CallInput) hooks.HookResult {
	if in == nil || len(in.History) == 0 {
		return hooks.HookResult{}
	}
	hasBoard, since := scanTaskBoard(in.History)
	if !hasBoard || since < h.Every {
		return hooks.HookResult{}
	}

	h.mu.Lock()
	last := h.nudgedAt[sessionID]
	if since < last+h.Every {
		h.mu.Unlock()
		return hooks.HookResult{}
	}
	h.nudgedAt[sessionID] = since
	h.mu.Unlock()

	// 追加到动态段末尾：静态前缀不变，KV 缓存仍然命中。
	in.SystemPromptSections = append(in.SystemPromptSections,
		gochatcore.NewSystemMessage(taskBoardNudgeText(since)))
	return hooks.HookResult{}
}

// AfterLLM 不干预：提醒时机只由请求组装前的历史快照决定。
func (h *TaskBoardNudge) AfterLLM(string, int, *hooks.LLMResponse, []hooks.ToolResult) hooks.HookResult {
	return hooks.HookResult{}
}

// Abort 是空实现。
func (h *TaskBoardNudge) Abort(string, string) {}

// isTaskBoardWriteTool 判断是否为「回写任务板」的调用。
// TaskList/TaskGet 只是读，读完不更新仍然算「没回写」，所以不计入。
func isTaskBoardWriteTool(name string) bool {
	return name == "TaskCreate" || name == "TaskUpdate"
}

// scanTaskBoard 扫历史得出：是否建过任务板，以及自最后一次回写之后的工具调用数。
//
// 只看 assistant 消息的 ToolCalls（工具名可靠，不依赖结果正文格式）；历史每次
// 组装都是同一份切片，扫描成本随历史线性，但只做一次字符串比较，可忽略。
func scanTaskBoard(history []session.Message) (hasBoard bool, since int) {
	total := 0
	lastWriteAt := -1
	for _, m := range history {
		for _, tc := range m.ToolCalls {
			total++
			if isTaskBoardWriteTool(tc.Name) {
				hasBoard = true
				lastWriteAt = total
			}
		}
	}
	if lastWriteAt < 0 {
		return hasBoard, 0
	}
	return hasBoard, total - lastWriteAt
}

func taskBoardNudgeText(since int) string {
	return fmt.Sprintf(
		"【任务板提醒】距上次回写任务板已过去 %d 次工具调用，而板上还有条目没推进。"+
			"如果当前工作仍属于这块任务板：先用 TaskList 读现状，把正在做的那一项 TaskUpdate 置为 in_progress 并在 active_form 写清「正在做什么」，做完立刻置 completed；"+
			"如果任务板已经不适用，就地收口（completed 或 cancelled）并说明原因。"+
			"任务板是用户判断进度的唯一依据，长期不回写等于给出静止的假进度。",
		since,
	)
}
