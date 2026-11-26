package agents

import (
	"context"
	"encoding/json"

	goagent "github.com/DotNetAge/goagent"
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/session"
)

// hookCoord 是换核后钩子体系的协调器：单次 Run 内由 goagent 内核顺序调用
// （BeforeLLM → LLM → AfterLLM → 工具执行 → 下一轮），全程单 goroutine，无需加锁。
//
// 职责：
//   - 聚合本轮工具执行结果（roundResults），供 LoopHook.AfterLLM 读取上一轮快照
//     （对齐旧核 execAfterLLMHooks 传 prevToolResults 的语义：AfterLLM 发生在
//     本轮工具执行之前，此时累积值即为上一轮结果）；
//   - 记录钩子中止信息（hookAbortReason/hookAbortAnswer/hookError）：
//     goagent 内核的中止原因不进事件流，由适配器拦截后回写至此，
//     供 runWithGoAgent 的终止原因映射消费；
//   - 承载重复错误引导计数（dupTracker）：引导话术注入必须发生在工具结果
//     进入 goagent 内核内存消息之前（回写期修改只影响持久化、不影响下一轮
//     LLM 请求），故由工具执行适配器在返回值层面注入；
//   - 收集图片视觉消息（imageEntries）：ImageHook 转换的图片块以 user 角色
//     消息回写进会话（见 writeBackNewMessages），按工具执行顺序配对。
type hookCoord struct {
	roundResults []hooks.ToolResult
	// toolSeq 是工具结果的全局序号（跨轮累计），图片消息回写按序号配对。
	toolSeq      int
	imageEntries []imageEntry

	hookAbortReason string
	// hookAbortAnswer 是 AfterLLM 中止时的答案文本（对齐旧核：AfterLLM 中止
	// 的答案是 LLM 本轮内容而非中止原因；BeforeLLM 中止为空，回退用中止原因）。
	hookAbortAnswer string
	hookError       error

	dupTracker dupErrorTracker
}

// imageEntry 记录一次携带图片的工具执行：图片块 + 对应的工具结果序号。
type imageEntry struct {
	toolSeq  int
	toolName string
	blocks   []session.ImageBlock
	// injected 表示图片已作为多模态消息注入当轮请求；注入后回写不再重复生成。
	injected bool
}

func newHookCoord() *hookCoord {
	return &hookCoord{
		dupTracker:   newDupErrorTracker(),
		imageEntries: []imageEntry{},
	}
}

// recordToolResult 在工具执行适配器完成 Hook 链后调用：追加本轮结果累积、
// 登记图片视觉消息（若有）并推进序号。
func (c *hookCoord) recordToolResult(tr hooks.ToolResult) {
	if len(tr.ImageBlocks) > 0 {
		c.imageEntries = append(c.imageEntries, imageEntry{
			toolSeq:  c.toolSeq,
			toolName: tr.ToolName,
			blocks:   tr.ImageBlocks,
		})
	}
	c.toolSeq++
	c.roundResults = append(c.roundResults, tr)
}

// carryOverImages 把上一次协调器里「登记了但还没注入上下文」的图片搬到新协调器。
//
// 挂起续跑会重建协调器（assemble），若图片尚未注入就被丢弃：Read 读图后紧接着
// 挂起（例如同一次迭代里还并行发起了 AskUser），图既没进上下文也不会再出现。
func (c *hookCoord) carryOverImages(from *hookCoord) {
	if from == nil {
		return
	}
	for _, e := range from.imageEntries {
		if e.injected || len(e.blocks) == 0 {
			continue
		}
		c.imageEntries = append(c.imageEntries, e)
	}
}

// snapshotRoundResults 取走本轮结果累积并清空（AfterLLM 快照语义：
// 调用后进入新一轮工具执行累积）。
func (c *hookCoord) snapshotRoundResults() []hooks.ToolResult {
	out := c.roundResults
	c.roundResults = nil
	return out
}

// hookTermination 报告钩子是否请求了中止（供终止原因映射判断优先级）。
func (c *hookCoord) hookTermination() bool {
	return c.hookError != nil || c.hookAbortReason != ""
}

// loopHookAdapter 将 goharness 的 hooks.LoopHook 适配为 goagent 的 LoopHook。
//
// 两侧接口差异与适配口径：
//   - goharness 显式传 sessionID/iteration + *CallInput（SystemPromptSections/
//     UserMessage/History/Tools）；goagent 只有 goagent.BeforeLLMInput。
//     适配器从内核消息序列反向构造 CallInput；
//   - 改写型钩子（修改已有 system 消息的 Content）：
//     钩子返回后适配器把修改后的 system 消息内容写回 input.Messages 对应位置
//     （共享底层数组，goagent 内核下一轮 LLM 调用可见）；
//   - History 为文本/工具调用级别的反向投影（goagent 内存消息不含
//     session.Message 的持久化字段如 Images，注入型钩子的判重逻辑仅依赖文本）；
//   - AfterLLM 入参 results 取 hookCoord 上一轮快照（对齐旧核 prevToolResults）；
//   - 中止结果：goharness HookResult 翻译为 goagent HookResult，并把中止信息
//     回写 hookCoord（goagent 内核的中止原因不进事件流）；
//   - Abort 转发：goagent 循环异常收尾处会以 "hook_abort:xxx"/"max_iterations"/
//     "error:xxx" 调用 Abort（正常完成不调用），与旧核 notifyLoopAbort 的
//     LIFO 语义等价（goagent 内部已按 LIFO 执行）。
type loopHookAdapter struct {
	inner      hooks.LoopHook
	sid        string
	agentName  string
	projectDir string
	coord      *hookCoord
	br         *eventBridge
}

// adaptLoopHooks 批量包装 goharness 循环钩子为 goagent 循环钩子。
func adaptLoopHooks(hs []hooks.LoopHook, sid, agentName, projectDir string, coord *hookCoord, br *eventBridge) []goagent.LoopHook {
	out := make([]goagent.LoopHook, 0, len(hs))
	for _, h := range hs {
		out = append(out, &loopHookAdapter{
			inner: h, sid: sid, agentName: agentName,
			projectDir: projectDir, coord: coord, br: br,
		})
	}
	return out
}

// Priority 直通内部钩子优先级（数值越小越先执行，两侧约定一致）。
func (a *loopHookAdapter) Priority() int { return a.inner.Priority() }

// BeforeLLM 构造 CallInput → 调用内部钩子 → 写回 system 段修改 → 翻译结果。
func (a *loopHookAdapter) BeforeLLM(ctx context.Context, input goagent.BeforeLLMInput) goagent.HookResult {
	callInput := a.buildCallInput(input)
	hr := a.inner.BeforeLLM(a.sid, input.Iteration, callInput)
	// 改写型钩子对 SystemPromptSections 的修改经此写回
	// 内核消息序列——gochatcore.Message 为值元素，写回 Content 切片头即更新
	// goagent 内存消息（底层数组共享，后续轮次的 LLM 请求可见修改）。
	a.writeBackSystemSections(input.Messages, callInput.SystemPromptSections)
	out := a.translate(hr, "")
	// 工具图片当轮注入：上一迭代工具执行时 ImageHook 登记了图片块，这里在下一次
	// 请求之前把它作为多模态消息追加到尾部——不等本 Run 结束回写，模型本轮的
	// 后续迭代就能看到图。追加的消息随后经回写落进会话历史（不再二次生成）。
	out.AppendMessages = append(out.AppendMessages, a.takeVisionMessages()...)
	return out
}

// takeVisionMessages 取走尚未注入的图片附件，转成尾部追加的多模态消息。
func (a *loopHookAdapter) takeVisionMessages() []gochatcore.Message {
	if a.coord == nil {
		return nil
	}
	var out []gochatcore.Message
	for i := range a.coord.imageEntries {
		e := &a.coord.imageEntries[i]
		if e.injected || len(e.blocks) == 0 {
			continue
		}
		blocks := make([]gochatcore.ContentBlock, 0, len(e.blocks)+1)
		blocks = append(blocks, gochatcore.ContentBlock{
			Type: gochatcore.ContentTypeText,
			Text: session.ToolVisionPrefix + " 工具 " + e.toolName + " 读取到的图像如下，请据此判断画面内容。",
		})
		for _, ib := range e.blocks {
			if block, ok := resolveImageBlock(ib); ok {
				blocks = append(blocks, block)
			}
		}
		if len(blocks) == 1 {
			// 全部图片都失效（文件缺失/空数据）：不注入空消息，也不标已注入，
			// 交给回写路径的现有降级处理。
			continue
		}
		e.injected = true
		out = append(out, gochatcore.Message{Role: "user", Content: blocks})
	}
	return out
}

// AfterLLM 构造 LLMResponse（含上一轮工具结果快照与本轮 usage）→ 调用内部钩子 → 翻译结果。
func (a *loopHookAdapter) AfterLLM(ctx context.Context, input goagent.AfterLLMInput) goagent.HookResult {
	resp := &hooks.LLMResponse{
		Content:      input.ResponseContent,
		Reasoning:    input.Reasoning,
		FinishReason: input.FinishReason,
	}
	for _, tc := range input.ToolCalls {
		inv := hooks.ToolCallInvocation{ID: tc.ID, Name: tc.Name}
		if tc.Arguments != "" {
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Arguments), &args); err == nil {
				inv.Arguments = args
			}
		}
		resp.ToolCalls = append(resp.ToolCalls, inv)
	}
	if a.br != nil {
		resp.TokenUsage = a.br.lastUsage
	}
	hr := a.inner.AfterLLM(a.sid, input.Iteration, resp, a.coord.snapshotRoundResults())
	return a.translate(hr, input.ResponseContent)
}

// Abort 转发中止通知（reason 由 goagent 内核给出，前缀语义见 loopHookAdapter 注释）。
func (a *loopHookAdapter) Abort(ctx context.Context, reason string) {
	a.inner.Abort(a.sid, reason)
}

// translate 将 goharness 钩子结果翻译为 goagent 结果，并把中止信息回写 hookCoord。
// llmContent 是 AfterLLM 中止时的答案文本（BeforeLLM 中止传空串）。
func (a *loopHookAdapter) translate(hr hooks.HookResult, llmContent string) goagent.HookResult {
	if hr.Error != nil {
		a.coord.hookError = hr.Error
		return goagent.HookResult{Error: hr.Error}
	}
	if hr.Abort {
		a.coord.hookAbortReason = hr.AbortReason
		a.coord.hookAbortAnswer = llmContent
		return goagent.HookResult{Abort: true, AbortReason: hr.AbortReason}
	}
	var appendMsgs []gochatcore.Message
	for _, m := range hr.AppendMessages {
		if converted, ok := sessionMessageToGochat(m); ok {
			appendMsgs = append(appendMsgs, converted)
		}
	}
	return goagent.HookResult{AppendMessages: appendMsgs}
}

// sessionMessageToGochat 把钩子要追加的会话消息转成内核消息。
// 只需要覆盖兜底注入会用的两种形态：纯文本（任意角色）与带图片的多模态 user 消息。
func sessionMessageToGochat(m session.Message) (gochatcore.Message, bool) {
	switch m.Role {
	case "assistant":
		msg := gochatcore.NewTextMessage("assistant", m.Content)
		msg.ReasoningContent = m.ReasoningContent
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, gochatcore.ToolCall{
				ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
			})
		}
		return msg, true
	case "user":
		if len(m.Images) == 0 {
			return gochatcore.NewUserMessage(m.Content), true
		}
		msg := gochatcore.Message{Role: "user"}
		if m.Content != "" {
			msg.Content = append(msg.Content, gochatcore.ContentBlock{
				Type: gochatcore.ContentTypeText, Text: m.Content,
			})
		}
		for _, img := range m.Images {
			block, ok := resolveImageBlock(img)
			if !ok {
				continue
			}
			msg.Content = append(msg.Content, block)
		}
		if len(msg.Content) == 0 {
			return gochatcore.Message{}, false
		}
		return msg, true
	case "system":
		return gochatcore.NewSystemMessage(m.Content), true
	default:
		return gochatcore.Message{}, false
	}
}

// buildCallInput 从 goagent 内核消息序列反向构造 goharness CallInput。
// system 消息拷贝进 SystemPromptSections（钩子修改后经 writeBackSystemSections 写回）；
// user/assistant/tool 消息投影为 session.Message 文本级 History。
func (a *loopHookAdapter) buildCallInput(input goagent.BeforeLLMInput) *hooks.CallInput {
	var systemSections []gochatcore.Message
	var history []session.Message
	userMessage := ""
	for _, m := range input.Messages {
		switch m.Role {
		case gochatcore.RoleSystem:
			systemSections = append(systemSections, m)
		case gochatcore.RoleUser:
			text := m.TextContent()
			history = append(history, session.Message{Role: "user", Content: text})
			userMessage = text
		case gochatcore.RoleAssistant:
			sm := session.Message{
				Role:             "assistant",
				Content:          m.TextContent(),
				ReasoningContent: m.ReasoningContent,
			}
			for _, tc := range m.ToolCalls {
				sm.ToolCalls = append(sm.ToolCalls, session.ToolCall{
					ID: tc.ID, Name: tc.Name, Arguments: tc.Arguments,
				})
			}
			history = append(history, sm)
		case gochatcore.RoleTool:
			history = append(history, session.Message{
				Role:       "tool",
				Content:    m.TextContent(),
				ToolCallID: m.ToolCallID,
			})
		}
	}
	return &hooks.CallInput{
		SessionID:            a.sid,
		AgentName:            a.agentName,
		ProjectDir:           a.projectDir,
		SystemPromptSections: systemSections,
		UserMessage:          userMessage,
		History:              history,
		Tools:                input.Tools,
	}
}

// writeBackSystemSections 把钩子修改后的 system 消息内容写回内核消息序列对应位置。
//
// 只能「改写」已存在的 system 消息：写回按序号与内核里的 system 消息配对，
// 钩子往 sections 末尾 append 的新段没有槽位，会被静默丢弃（第一版任务板提醒
// 就是这么失效的）。丢弃一律留 WARN，避免再次出现「看起来注入了、其实没进请求」。
// 需要追加新内容请用 HookResult.AppendMessages（真实消息追加到对话尾部）。
func (a *loopHookAdapter) writeBackSystemSections(messages []gochatcore.Message, sections []gochatcore.Message) {
	if len(sections) == 0 {
		return
	}
	si := 0
	for i := range messages {
		if messages[i].Role == gochatcore.RoleSystem && si < len(sections) {
			messages[i].Content = sections[si].Content
			si++
		}
	}
	if len(sections) > si && a.br != nil && a.br.rt != nil {
		a.br.rt.logger.Warn("钩子追加的 system 段没有落点，已被丢弃",
			"session", a.sid, "dropped", len(sections)-si,
			"hint", "兜底注入请改用 HookResult.AppendMessages")
	}
}
