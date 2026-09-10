package agents

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/session"
)

// ── 压缩失效观测测试 ──────────────────────────────────────────────────────
//
// 背景（2026-09-09 会话 01M228K31ES58TP9J17AFD12QR 的生产日志铁证）：
//   13:52:14 ForceCompact 触发：active_messages=45，window_tokens=145946
//   13:52:14 压缩请求构造完成：msg_count=47，tool_count=25，instruction_len=4166
//   13:56:17 压缩响应为空（4 分 2 秒后），cursor 未移动，压缩失效
//   同会话 13:44 主对话 iter=10 同样空响应：answer_len=0，stream.Usage() 为 nil
//
// 本测试把该会话的真实数据独立固化为夹具，观测两个层面：
//   1. 离线：压缩请求构造层——用真实数据重建请求，对齐日志证据；
//   2. 在线（MINDX_RUN_LIVE=1 门控）：真实调用 LLM，完整收集 compactor.go
//      生产路径中丢弃的事件信息（thinking、FinishReason、Usage），
//      定位「4 分钟空响应」的根因。

// fixturePaths 观测夹具位置（由真实问题会话导出，content 已 base64 解码）。
var (
	fixtureMessages = "testdata/compaction-failure-session.json"
	fixtureSystem   = "testdata/compaction-system-prompt.txt"
)

// loadFixtureMessages 加载真实会话消息夹具。
func loadFixtureMessages(t *testing.T) []session.Message {
	t.Helper()
	data, err := os.ReadFile(fixtureMessages)
	if err != nil {
		t.Fatalf("加载消息夹具失败: %v", err)
	}
	var msgs []session.Message
	if err := json.Unmarshal(data, &msgs); err != nil {
		t.Fatalf("解析消息夹具失败: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("消息夹具为空")
	}
	return msgs
}

// loadFixtureSystemPrompt 加载压缩时刻的真实 system prompt（取自 daemon 日志）。
func loadFixtureSystemPrompt(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(fixtureSystem)
	if err != nil {
		t.Fatalf("加载 system prompt 夹具失败: %v", err)
	}
	return strings.TrimSpace(string(data))
}

// reportToolPairing 观测消息序列中 tool_call 与 tool 结果的配对情况。
// 返回 [assistant 携带的 tool_call 总数, tool 消息总数, 孤立 tool 消息数]。
// 孤立 tool 会被 stripOrphanedToolCalls / sanitizeMessagesForLLM 剥离，
// 是请求构造层可能「静默丢消息」的位置。
func reportToolPairing(msgs []session.Message) (calls, toolMsgs, orphaned int) {
	expected := make(map[string]bool)
	for _, m := range msgs {
		for _, tc := range m.ToolCalls {
			expected[tc.ID] = true
			calls++
		}
	}
	for _, m := range msgs {
		if m.Role != "tool" {
			continue
		}
		toolMsgs++
		if !expected[m.ToolCallID] {
			orphaned++
		}
	}
	return calls, toolMsgs, orphaned
}

// TestCompactionRequestReconstruction 离线观测：用真实失效数据重建压缩请求。
// 断言请求消息数与生产日志 msg_count=47 对齐，并输出请求画像。
func TestCompactionRequestReconstruction(t *testing.T) {
	// 日志铁证对齐：压缩指令长度必须仍是 4166 字符
	if len(compactionInstruction) != 4166 {
		t.Errorf("compactionInstruction 长度 = %d，生产日志为 4166，指令文本已漂移", len(compactionInstruction))
	}

	history := loadFixtureMessages(t)
	if len(history) != 45 {
		t.Errorf("夹具消息数 = %d，生产日志 active_messages=45", len(history))
	}

	// 与 compactionTurn 同构：真实 system + 原始历史（不重排）+ 末尾追加压缩指令
	systemMsgs := []gochatcore.Message{gochatcore.NewSystemMessage(loadFixtureSystemPrompt(t))}
	msgs := AssembleMessages(systemMsgs, history, "")
	msgs = append(msgs, gochatcore.NewUserMessage(compactionInstruction))

	// 日志铁证对齐：压缩请求消息总数必须仍是 47（45 会话 + 1 system + 1 指令）。
	// 若 stripOrphanedToolCalls 剥离了孤立消息，此处会偏离并暴露构造层丢消息。
	if len(msgs) != 47 {
		t.Errorf("重建请求消息数 = %d，生产日志 msg_count=47", len(msgs))
	}

	// 请求画像
	calls, toolMsgs, orphaned := reportToolPairing(history)
	var totalChars int
	roleCount := map[string]int{}
	for _, m := range history {
		roleCount[m.Role]++
		totalChars += len(m.Content) + len(m.ReasoningContent)
		for _, tc := range m.ToolCalls {
			totalChars += len(tc.Arguments)
		}
	}
	totalChars += len(loadFixtureSystemPrompt(t)) + len(compactionInstruction)

	t.Logf("请求画像：消息数=%d（含 system/instruction），会话角色分布=%v", len(msgs), roleCount)
	t.Logf("tool_call=%d 个，tool 消息=%d 条，孤立 tool=%d 条", calls, toolMsgs, orphaned)
	t.Logf("请求总字符量≈%d（≈%d 万字符），估算输入 tokens≈%d", totalChars, totalChars/10000, totalChars*3/10)

	// 末尾结构观测：生产数据末尾是连续 5 条 tool 消息，压缩指令紧跟其后。
	// 这是「思考型模型 + 工具消息收尾 + 指令要求禁用工具」的组合输入形态，
	// 与 glm-4.7-flash 空响应现象强相关，重建必须保持一致。
	tail := ""
	for i := len(history) - 3; i < len(history); i++ {
		tail += history[i].Role + "/"
	}
	t.Logf("会话末尾 3 条消息角色: %s（后接压缩指令 user）", tail)

	// assistant 消息 content 空心化观测：思考型模型把内容写进 reasoning_content，
	// content 近乎全空。压缩器只收集 content 流，若模型把摘要也全写进思考流，
	// compactor 收集到的 content 就是空——这是空响应的直接通道。
	emptyContent := 0
	for _, m := range history {
		if m.Role == "assistant" && strings.TrimSpace(m.Content) == "" {
			emptyContent++
		}
	}
	t.Logf("assistant 消息 content 为空的比例: %d/%d", emptyContent, roleCount["assistant"])
}

// liveObservationStats 在线观测的流事件统计。
type liveObservationStats struct {
	firstEventDelay time.Duration // 首事件延迟
	totalDuration   time.Duration // 流总耗时
	contentLen      int           // content 累计字符数
	thinkingLen     int           // thinking（思考流）累计字符数
	toolCallDeltas  int           // tool_call 增量事件数
	doneCount       int           // done 事件数
	errorCount      int           // error 事件数
	errMsg          string        // 最后一个错误信息
	finishReason    string        // done 事件携带的终止原因
	eventUsage      *gochatcore.Usage
	thinkingPreview string // thinking 开头预览（content 为空时用于判定模型行为）
}

// loadLiveAPIKey 获取在线观测用的智谱 API Key：
// 优先环境变量 MINDX_TEST_BIGMODEL_KEY；macOS 上回退到系统 Keychain
// （service=mindx，account=bigmodel，与 daemon 的自定义密钥存储同源）。
// 取不到时返回空串，由调用方 Skip。
func loadLiveAPIKey(t *testing.T) string {
	t.Helper()
	if k := os.Getenv("MINDX_TEST_BIGMODEL_KEY"); k != "" {
		return k
	}
	if runtime.GOOS != "darwin" {
		return ""
	}
	out, err := exec.Command("security", "find-generic-password", "-s", "mindx", "-a", "bigmodel", "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// TestCompactionLiveObservation 在线观测：用真实失效数据真实调用 LLM。
//
// 生产 compactor 的流消费循环（compactor.go）只收集 content，丢弃了
// thinking 内容、FinishReason 与 Usage——空响应发生时无任何线索可查。
// 本测试用同一份请求做完整观测，补齐全部丢弃信息：
//
//	MINDX_RUN_LIVE=1 go test -run TestCompactionLiveObservation -timeout 15m ./agents/ -v
//
// 默认（不设 MINDX_RUN_LIVE）跳过，避免单元测试打出真实 API 请求。
// 与生产压缩请求的唯一已知差异：未携带 25 个工具定义（工具清单由 Runtime 注册表生成，
// 观测层不引入该依赖）；生产日志已证实空响应轮次模型未产生 tool_call，该差异不影响观测目标。
func TestCompactionLiveObservation(t *testing.T) {
	if os.Getenv("MINDX_RUN_LIVE") != "1" {
		t.Skip("在线观测需真实调用 LLM：MINDX_RUN_LIVE=1 go test -run TestCompactionLiveObservation -timeout 15m ./agents/ -v")
	}
	apiKey := loadLiveAPIKey(t)
	if apiKey == "" {
		t.Skip("未获取到智谱 API Key（环境变量 MINDX_TEST_BIGMODEL_KEY 或 macOS Keychain service=mindx account=bigmodel）")
	}
	t.Logf("API Key 已加载（长度 %d，不打印明文）", len(apiKey))

	history := loadFixtureMessages(t)
	systemMsgs := []gochatcore.Message{gochatcore.NewSystemMessage(loadFixtureSystemPrompt(t))}
	msgs := AssembleMessages(systemMsgs, history, "")
	msgs = append(msgs, gochatcore.NewUserMessage(compactionInstruction))

	// 请求字段与生产 compactionTurn 逐项一致：
	// glm-4.7-flash / temperature=0.7（models.yml）/ ToolChoice=auto / 压缩超时 10min。
	// EnableThinking(true) 在 NewDefaultLLMClient.buildStream 内固定开启，此处无需重复设置。
	client := NewDefaultLLMClient(apiKey, "https://open.bigmodel.cn/api/paas/v4", "bigmodel")
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()

	start := time.Now()
	stream, err := client.Stream(ctx, LLMRequest{
		Messages:    msgs,
		Model:       "glm-4.7-flash",
		Temperature: 0.7,
		ToolChoice:  "auto",
		Timeout:     compactionTimeout,
	})
	if err != nil {
		t.Fatalf("建流失败: %v", err)
	}
	defer stream.Close()

	var st liveObservationStats
	firstEventAt := time.Time{}
	for stream.Next() {
		ev := stream.Event()
		if firstEventAt.IsZero() {
			firstEventAt = time.Now()
			st.firstEventDelay = firstEventAt.Sub(start)
		}
		switch ev.Type {
		case gochatcore.EventContent:
			st.contentLen += len(ev.Content)
		case gochatcore.EventThinking:
			st.thinkingLen += len(ev.Content)
			if len(st.thinkingPreview) < 500 {
				st.thinkingPreview += ev.Content
			}
		case gochatcore.EventToolCall:
			st.toolCallDeltas += len(ev.ToolCallDeltas)
		case gochatcore.EventDone:
			st.doneCount++
			st.finishReason = ev.FinishReason
			st.eventUsage = ev.Usage
		case gochatcore.EventError:
			st.errorCount++
			st.errMsg = ev.Err.Error()
		}
	}
	st.totalDuration = time.Since(start)

	// 观测报告：生产路径丢弃的全部信息在此完整呈现
	t.Logf("── 流事件观测报告 ──")
	t.Logf("总耗时: %v（生产日志为 4m2s）", st.totalDuration.Round(time.Millisecond))
	t.Logf("首事件延迟: %v", st.firstEventDelay.Round(time.Millisecond))
	t.Logf("事件统计: content=%d 字符, thinking=%d 字符, tool_call 增量=%d 个, done=%d 次, error=%d 次",
		st.contentLen, st.thinkingLen, st.toolCallDeltas, st.doneCount, st.errorCount)
	if st.errMsg != "" {
		t.Logf("流内错误: %s", st.errMsg)
	}
	t.Logf("FinishReason: %q（done 事件携带，生产 compactor 丢弃）", st.finishReason)
	if st.eventUsage != nil {
		t.Logf("流事件 Usage: prompt=%d completion=%d total=%d %+v（生产 compactor 丢弃）",
			st.eventUsage.PromptTokens, st.eventUsage.CompletionTokens, st.eventUsage.TotalTokens, st.eventUsage)
	} else {
		t.Logf("流事件 Usage: nil")
	}
	if u := stream.Usage(); u != nil {
		t.Logf("Stream.Usage(): %+v", u)
	} else {
		t.Logf("Stream.Usage(): nil（与生产日志 executor.go:986 「Usage 返回 nil」现象一致）")
	}
	if tc := stream.ToolCalls(); len(tc) > 0 {
		t.Logf("模型产生了 %d 个 tool_call（生产 compactor 只记日志不利用）:", len(tc))
		for _, c := range tc {
			t.Logf("  - %s %s args=%dB", c.Name, c.ID, len(c.Arguments))
		}
	}

	// 根因判定提示（观测性输出，不做硬断言——模型行为存在天然波动）
	if st.contentLen == 0 {
		t.Logf("[观测确认] content 为空——压缩必然失效（compactor 只收集 content）")
		switch {
		case st.finishReason == "length":
			t.Logf("[根因指向] finish_reason=length：输出 token 预算被思考流耗尽，content 一个字都没轮到")
		case st.thinkingLen > 0:
			t.Logf("[根因指向] 模型输出了 %d 字符思考流但 content 为空，finish_reason=%q", st.thinkingLen, st.finishReason)
		case st.errorCount > 0:
			t.Logf("[根因指向] 流内错误终止: %s", st.errMsg)
		default:
			t.Logf("[根因指向] 无内容无错误，finish_reason=%q——疑似服务端静默截断", st.finishReason)
		}
		if st.thinkingPreview != "" {
			preview := st.thinkingPreview
			if len(preview) > 500 {
				preview = preview[:500]
			}
			t.Logf("思考流开头预览: %s", preview)
		}
	}
}
