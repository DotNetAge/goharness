package reactor_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"errors"
	"github.com/DotNetAge/goreact/core"
	"github.com/DotNetAge/goreact/internal/reactor/hooks/action"
	"github.com/DotNetAge/goreact/reactor"
)

// ============================================================================
// Integration Test Infrastructure
// ============================================================================

type intEventCollector struct {
	mu     sync.Mutex
	events []core.ReactEvent
	types  map[core.ReactEventType]bool
	ch     <-chan core.ReactEvent
	cancel func()
}

func newIntEventCollector(bus reactor.EventBus, types ...core.ReactEventType) *intEventCollector {
	typeSet := make(map[core.ReactEventType]bool, len(types))
	for _, typ := range types {
		typeSet[typ] = true
	}
	ch, cancel := bus.SubscribeFiltered(func(e core.ReactEvent) bool {
		return typeSet[e.Type]
	})
	return &intEventCollector{
		events: make([]core.ReactEvent, 0),
		types:  typeSet,
		ch:     ch,
		cancel: cancel,
	}
}

func (c *intEventCollector) drain(timeout time.Duration) []core.ReactEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-c.ch:
			if !ok {
				return c.events
			}
			c.events = append(c.events, ev)
		case <-deadline:
			return c.events
		}
	}
}

func (c *intEventCollector) close() {
	if c.cancel != nil {
		c.cancel()
	}
}

func (c *intEventCollector) countByType(typ core.ReactEventType) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func (c *intEventCollector) getByType(typ core.ReactEventType) []core.ReactEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []core.ReactEvent
	for _, e := range c.events {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

type intMockTool struct {
	name   string
	result string
}

func (t *intMockTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:        t.name,
		Description: fmt.Sprintf("Mock tool %s", t.name),
		Parameters: []core.Parameter{
			{Name: "query", Type: "string", Required: true},
		},
		SecurityLevel: core.LevelSafe,
		IsReadOnly:    true,
	}
}

func (t *intMockTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	q, _ := params["query"].(string)
	if t.result != "" {
		return t.result, nil
	}
	return fmt.Sprintf("[%s result: %s]", t.name, q), nil
}

type intReadOnlyTool struct{}

func (t *intReadOnlyTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:          "read_file",
		Description:   "Read file contents safely",
		Parameters:    []core.Parameter{{Name: "path", Type: "string", Required: true}},
		SecurityLevel: core.LevelSafe,
		IsReadOnly:    true,
	}
}

func (t *intReadOnlyTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	path, _ := params["path"].(string)
	return "File content of " + path + ": package main; func main() {}", nil
}

func newIntTestReactor(mockFn reactor.MockLLMFunc, extraTools ...core.FuncTool) (*reactor.Reactor, reactor.EventBus, *intEventCollector) {
	bus := reactor.NewEventBus()
	cfg := reactor.ReactorConfig{Model: "qwen3.6-plus", MaxIterations: 15}
	r := reactor.NewReactor(cfg, reactor.WithMockLLM(mockFn), reactor.WithEventBus(bus), reactor.WithoutBundledTools())
	r.RegisterThoughtHooks(&intPreCheckHook{})
	r.RegisterToolHooks(&action.ToolEventHook{})
	r.RegisterObservationHooks(&intConvergenceHook{})
	for _, tool := range extraTools {
		if err := r.RegisterTool(tool); err != nil {
			panic(fmt.Sprintf("register tool failed: %v", err))
		}
	}
	collector := newIntEventCollector(bus,
		core.ActionStart, core.ToolExecStart, core.ToolExecEnd,
		core.ActionEnd, core.PermissionDenied, core.FinalAnswer,
	)
	return r, bus, collector
}

type intPreCheckHook struct{}

func (h *intPreCheckHook) Priority() int { return reactor.PriorityPreCheck }
func (h *intPreCheckHook) Before(ctx *reactor.ReactContext, input *reactor.CallInput) reactor.HookResult {
	if ctx.CurrentIteration >= ctx.MaxIterations {
		return reactor.HookResult{Abort: true, AbortReason: "max iterations"}
	}
	return reactor.HookResult{}
}
func (h *intPreCheckHook) After(ctx *reactor.ReactContext, thought *reactor.Thought) reactor.HookResult {
	return reactor.HookResult{}
}
func (h *intPreCheckHook) Abort(ctx *reactor.ReactContext, reason string) {}

type intConvergenceHook struct{}

func (h *intConvergenceHook) Priority() int { return reactor.PriorityConvergence }
func (h *intConvergenceHook) After(ctx *reactor.ReactContext, obs *reactor.Observation) reactor.HookResult {
	t := ctx.LastThought
	if t != nil && t.Decision == reactor.DecisionAnswer {
		return reactor.HookResult{Abort: true, AbortReason: "answer produced"}
	}
	return reactor.HookResult{}
}
func (h *intConvergenceHook) Abort(ctx *reactor.ReactContext, reason string) {}

// ============================================================================
// SCENARIO A: Sliding Window — Multi-Round Complex Analysis Triggers Slide
// 场景：生产环境严重服务降级事故分析（Redis Cluster MOVED错误→根因定位）
// 验证：多轮迭代中每轮ActionStart/ToolExecStart正确触发、滑动窗口保留system消息
// ============================================================================

func TestIntegration_SlidingWindow_MultiRoundComplexAnalysis(t *testing.T) {
	userQuestion := "我们的生产环境昨晚22:30发生了严重的服务降级事故：用户下单接口P99延迟从200ms飙升到12s，支付回调超时，库存服务拒绝连接（pool exhausted），日志显示大量Redis Cluster MOVED错误。请分析根因链路，定位故障起点，给出恢复建议。"

	iterCount := 0
	r, _, collector := newIntTestReactor(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		iterCount++
		switch iterCount {
		case 1:
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "web_search", Arguments: "{\"query\":\"Redis Cluster MOVED ASK redirect golang redis client\"}"}},
			}}, nil
		case 2:
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "web_fetch", Arguments: "{\"url\":\"https://internal-dashboard.ops.example.com/incident/i-20260220\"}"}},
			}}, nil
		case 3:
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "bash", Arguments: "{\"cmd\":\"redis-cli --cluster info\"}"}},
			}}, nil
		case 4:
			answerA := "{\"decision\":\"answer\",\"final_answer\":\"## 根因分析报告\\n\\n故障时间线:\\n- 22:25 Redis Cluster slot migration began\\n- 22:27 pay-gw received ASK redirects\\n- 22:28 go-redis/v9 client bug blocked on ASK handling\\n- 22:30 Connection pool exhausted, cascade to order-svc/inv-svc\\n\\n根因: pay-gw go-redis/v9 v9.0.47 has known issue #2341 where ASK response blocks instead of async retry.\\n\\n修复建议:\\n1. Upgrade to v9.0.51+\\n2. Increase connection pool max-active to 200\\n3. Move Redis maintenance window to 02:00-04:00\",\"reasoning\":\"Analysis complete after %d rounds.\"}"
			return &gochatcore.Response{Content: fmt.Sprintf(answerA, iterCount)}, nil
		default:
			return &gochatcore.Response{Content: "{\"decision\":\"answer\",\"final_answer\":\"Done.\"}"}, nil
		}
	}, &intMockTool{name: "web_search"}, &intMockTool{name: "web_fetch"}, &intMockTool{name: "bash"})

	cw := core.NewContextWindow("slide-test-session", 2000)
	r.SetContextWindow(cw)

	result, err := r.Run(context.Background(), userQuestion, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	_ = collector.drain(3 * time.Second)
	collector.close()

	t.Logf("=== SCENARIO A: Sliding Window Multi-Round Analysis ===")
	t.Logf("User question: 生产环境Redis Cluster MOVED导致P99从200ms飙到12s")
	t.Logf("Iterations: %d, Messages=%d, Tokens=%d/%d, Ratio=%.3f",
		result.TotalIterations, cw.MessageCount(), cw.TokensUsed, cw.MaxTokens, cw.UsageRatio())

	asCount := collector.countByType(core.ActionStart)
	tsCount := collector.countByType(core.ToolExecStart)
	teCount := collector.countByType(core.ToolExecEnd)
	t.Logf("ActionStart=%d, ToolExecStart=%d, ToolExecEnd=%d", asCount, tsCount, teCount)

	if result.TotalIterations < 4 {
		t.Errorf("expected >= 4 iterations (3 tool rounds + 1 answer), got %d", result.TotalIterations)
	}
	if asCount < 3 {
		t.Errorf("expected >=3 ActionStart (one per tool iteration), got %d", asCount)
	}
	if tsCount < 3 {
		t.Errorf("expected >=3 ToolExecStart, got %d", tsCount)
	}
	if tsCount != teCount {
		t.Errorf("ToolExecStart(%d) != ToolExecEnd(%d), every started tool should end", tsCount, teCount)
	}
	if cw.MessageCount() < 4 {
		t.Errorf("Messages=%d below MinPreserveMessages=4 threshold", cw.MessageCount())
	}

	actionStarts := collector.getByType(core.ActionStart)
	for i, as := range actionStarts {
		data := as.Data.(core.ActionStartData)
		t.Logf("  ActionStart[%d]: Iter=%d, Tools=%d, Names=%v", i, data.Iteration, data.ToolCount, data.ToolNames)
		if data.ToolCount <= 0 {
			t.Errorf("ActionStart[%d]: ToolCount should be >0, got %d", i, data.ToolCount)
		}
		if len(data.ToolNames) == 0 {
			t.Errorf("ActionStart[%d]: ToolNames should not be empty", i)
		}
	}
}

// ============================================================================
// SCENARIO B: Sliding Window — Multiple Rounds Message Correctness
// 场景：10万行Java单体应用拆分微服务（涉及DB拆分/分布式事务/RPC/配置管理）
// 验证：多轮大响应后滑动窗口正常工作、消息保持时间序、UsageRatio在合理范围
// ============================================================================

func TestIntegration_SlidingWindow_MultipleSlides_MessageCorrectness(t *testing.T) {
	userQuestion := "我正在把一个10万行的单体Java应用拆分成微服务架构。问题包括：1)共享数据库拆分策略（外键关联）2)分布式事务方案（Seata TCC vs 消息表 vs Saga）3)服务间通信（gRPC vs Dubbo vs Feign）4)配置管理（Nacos vs Apollo vs Consul）。请逐一给出建议和代码示例。"

	callCount := 0
	r, _, collector := newIntTestReactor(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		callCount++
		topics := []string{"db splitting", "distributed transaction", "rpc comparison", "config management", "service discovery", "observability"}
		topic := topics[(callCount-1)%len(topics)]
		if callCount <= 7 {
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "web_search", Arguments: "{\"query\":\"microservices " + topic + " best practices 2024\"}"}},
			}}, nil
		}
		return &gochatcore.Response{Content: "{\"decision\":\"answer\",\"final_answer\":\"Based on 7 rounds of analysis, here is the complete microservices migration roadmap with code examples for each area.\"}"}, nil
	}, &intMockTool{name: "web_search"})

	maxTokens := int64(800)
	cw := core.NewContextWindow("multi-slide", maxTokens)
	r.SetContextWindow(cw)

	_, err := r.Run(context.Background(), userQuestion, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	_ = collector.drain(3 * time.Second)
	collector.close()

	t.Logf("=== SCENARIO B: Multiple Slides Message Correctness ===")
	t.Logf("User question: 10万行Java单体拆微服务（4个技术维度逐一分析）")
	t.Logf("LLM Calls=%d, Messages=%d, Tokens=%d/%d, Ratio=%.3f",
		callCount, cw.MessageCount(), cw.TokensUsed, cw.MaxTokens, cw.UsageRatio())

	if cw.MessageCount() < 4 {
		t.Errorf("MessageCount=%d < MinPreserve=4 after sliding", cw.MessageCount())
	}

	asCount := collector.countByType(core.ActionStart)
	t.Logf("ActionStart events collected: %d", asCount)
	if asCount < 5 {
		t.Errorf("expected >=5 ActionStart for 7+ LLM calls with tools, got %d", asCount)
	}

	msgs := cw.GetMessages()
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Timestamp < msgs[i-1].Timestamp {
			t.Errorf("messages not chronological at index %d: msg[%d].ts=%d < msg[%d].ts=%d",
				i, i, msgs[i].Timestamp, i-1, msgs[i-1].Timestamp)
		}
	}
	if cw.UsageRatio() > 10.0 {
		t.Logf("Note: UsageRatio=%.3f very high — sliding may not have reduced enough", cw.UsageRatio())
	}
}

// ============================================================================
// SCENARIO C: Permission — Allow Then Execute Successfully
// 场景：部署到staging环境并跑smoke test（权限全部Allow）
// 验证：Allow路径下工具正常执行、无PermissionDenied事件、最终答案包含执行结果
// ============================================================================

func TestIntegration_Permission_AllowThenExecute(t *testing.T) {
	userQuestion := "帮我把main分支部署到staging环境(staging-03节点)，镜像标签latest，部署后跑smoke test，通过则切换流量否则回滚。"

	iterCount := 0
	r, _, collector := newIntTestReactor(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		iterCount++
		switch iterCount {
		case 1:
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "deploy", Arguments: "{\"env\":\"staging\",\"node\":\"staging-03\",\"image_tag\":\"latest\",\"strategy\":\"rolling\"}"}},
			}}, nil
		case 2:
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "bash", Arguments: "{\"cmd\":\"kubectl exec deployment/api-server -n staging -- /app/smoke-test\"}"}},
			}}, nil
		case 3:
			return &gochatcore.Response{Content: "{\"decision\":\"answer\",\"final_answer\":\"## Staging Deployment Report\\n\\nDeployed: staging-03, image latest, rolling update, took 47s.\\nSmoke Test: Health PASS, DB PASS, Redis PASS, Auth PASS, Cache 90%.\\nTraffic switched to new version. Rollback ready: kubectl rollout undo deployment/api-server -n staging\"}"}, nil
		default:
			return &gochatcore.Response{Content: "{\"decision\":\"answer\",\"final_answer\":\"Done.\"}"}, nil
		}
	}, &intMockTool{name: "deploy"}, &intMockTool{name: "bash"})

	allowChain := core.NewPermissionChain(&allowStagingOpsChecker{})
	r.RegisterToolHooks(&action.PermissionHook{Chain: allowChain})

	result, err := r.Run(context.Background(), userQuestion, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	_ = collector.drain(3 * time.Second)
	collector.close()

	t.Logf("=== SCENARIO C: Permission Allow Then Execute ===")
	t.Logf("User question: 部署main到staging-03节点+smoke test")
	t.Logf("Iterations: %d, Answer length: %d", result.TotalIterations, len(result.Answer))

	if result.TotalIterations < 3 {
		t.Errorf("expected 3 iterations (deploy+smoke+answer), got %d", result.TotalIterations)
	}
	tsCount := collector.countByType(core.ToolExecStart)
	teCount := collector.countByType(core.ToolExecEnd)
	t.Logf("ToolExecStart=%d, ToolExecEnd=%d", tsCount, teCount)
	if tsCount != 2 {
		t.Errorf("expected 2 ToolExecStart (deploy+bash), got %d", tsCount)
	}
	if tsCount != teCount {
		t.Errorf("ToolExecStart(%d) should equal ToolExecEnd(%d)", tsCount, teCount)
	}
	deniedCount := collector.countByType(core.PermissionDenied)
	if deniedCount > 0 {
		t.Errorf("Expected 0 PermissionDenied when all allowed, got %d", deniedCount)
	}
	if !strings.Contains(result.Answer, "PASS") {
		t.Error("Final answer should confirm smoke test passed")
	}

	actionStarts := collector.getByType(core.ActionStart)
	for i, as := range actionStarts {
		data := as.Data.(core.ActionStartData)
		t.Logf("  ActionStart[%d]: Iter=%d, Tools=%d, Names=%v", i, data.Iteration, data.ToolCount, data.ToolNames)
	}
}

// ============================================================================
// SCENARIO G: Permission Deny — Denied Tool Does Not Execute
// 场景：用户请求高危操作（删除数据库），权限系统明确 Deny
// 验证：PermissionDenied 事件发射，ToolExecStart 不发射，最终答案不包含执行结果
// ============================================================================

func TestIntegration_Permission_Deny(t *testing.T) {
	userQuestion := "帮我把生产环境的redis数据全部清掉，跑一下 FLUSHALL"

	iterCount := 0
	r, _, collector := newIntTestReactor(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		iterCount++
		if iterCount == 1 {
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "bash", Arguments: `{"cmd":"redis-cli FLUSHALL"}`}},
			}}, nil
		}
		// After seeing the denial feedback, produce an answer to terminate
		return &gochatcore.Response{
			Content: `{"decision":"answer","final_answer":"FLUSHALL已被权限系统拒绝，未执行任何操作。","reasoning":"高危操作已被拒绝"}`,
		}, nil
	}, &intMockTool{name: "bash"})

	// Register a permission chain that denies "bash"
	denyChain := core.NewPermissionChain(&denyBashChecker{})
	r.RegisterToolHooks(&action.PermissionHook{Chain: denyChain})

	result, err := r.Run(context.Background(), userQuestion, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	_ = collector.drain(3 * time.Second)
	collector.close()

	t.Logf("=== SCENARIO G: Permission Deny ===")
	t.Logf("User question: 请求高危操作 FLUSHALL")
	t.Logf("Iterations: %d, Termination: %s", result.TotalIterations, result.TerminationReason)

	// ToolExecStart should NOT be emitted (denied before execution, PermissionHook
	// returns Abort before calling toolExecutor.Execute)
	tsCount := collector.countByType(core.ToolExecStart)
	if tsCount != 0 {
		t.Errorf("Expected 0 ToolExecStart (tool denied before execution), got %d", tsCount)
	}

	// PermissionDenied event must be emitted (now emitted by PermissionHook.Before)
	deniedCount := collector.countByType(core.PermissionDenied)
	if deniedCount < 1 {
		t.Errorf("Expected >=1 PermissionDenied event, got %d", deniedCount)
	}

	// Should complete in 2 iterations: tool call denied → answer
	if result.TotalIterations != 2 {
		t.Errorf("Expected 2 iterations (denied tool + answer), got %d", result.TotalIterations)
	}

	t.Logf("PermissionDenied events: %d, ToolExecStart: %d, TotalIterations: %d",
		deniedCount, tsCount, result.TotalIterations)
}

type denyBashChecker struct{}

func (h *denyBashChecker) CheckPermissions(ctx *core.ToolUseContext) core.PermissionResult {
	if ctx.ToolName == "bash" {
		return core.PermissionResult{
			Behavior: core.PermissionDeny,
			Message:  "bash execution is not allowed in this read-only audit scenario",
		}
	}
	return core.PermissionResult{Behavior: core.PermissionAllow}
}

// ============================================================================
// SCENARIO H: Tool Execution Error — Sync Tool Returns Error
// 场景：工具执行过程中出现运行时错误（网络超时、文件不存在等）
// 验证：ToolExecEnd 发射携带错误信息、ActionEnd 错误计数正确、Observe 产生错误观测
// ============================================================================

func TestIntegration_ToolExecError_SyncTool(t *testing.T) {
	userQuestion := "帮我检查一下服务器的 /etc/hosts 文件"

	iterCount := 0
	r, _, collector := newIntTestReactor(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		iterCount++
		if iterCount == 1 {
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "file_check", Arguments: `{"path":"/etc/hosts"}`}},
			}}, nil
		}
		// After the error is fed back, produce an answer to terminate
		return &gochatcore.Response{
			Content: `{"decision":"answer","final_answer":"检查失败：/etc/hosts 文件访问被拒绝，权限不足或文件不存在。"}`,
		}, nil
	}, &intErrorTool{name: "file_check"})

	result, err := r.Run(context.Background(), userQuestion, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	_ = collector.drain(3 * time.Second)
	collector.close()

	t.Logf("=== SCENARIO H: Tool Execution Error ===")
	t.Logf("User question: 检查 /etc/hosts 文件")
	t.Logf("Iterations: %d, Termination: %s", result.TotalIterations, result.TerminationReason)

	// First iteration: ToolExecStart + ToolExecEnd with error
	tsCount := collector.countByType(core.ToolExecStart)
	teCount := collector.countByType(core.ToolExecEnd)
	t.Logf("ToolExecStart=%d, ToolExecEnd=%d", tsCount, teCount)
	if tsCount != 1 {
		t.Errorf("Expected 1 ToolExecStart, got %d", tsCount)
	}
	if tsCount != teCount {
		t.Errorf("ToolExecStart(%d) != ToolExecEnd(%d)", tsCount, teCount)
	}

	// Verify the ToolExecEnd contains error info
	teEvents := collector.getByType(core.ToolExecEnd)
	if len(teEvents) > 0 {
		data := teEvents[0].Data.(core.ToolExecEndData)
		t.Logf("  ToolExecEnd: Name=%s, Success=%v, Error=%s", data.ToolName, data.Success, data.Error)
		if data.Success {
			t.Errorf("ToolExecEnd should have Success=false, got true")
		}
		if data.Error == "" {
			t.Errorf("ToolExecEnd should contain error message, got empty")
		}
	}

	// ActionEnd should reflect the failure (0/1 success)
	aeEvents := collector.getByType(core.ActionEnd)
	if len(aeEvents) > 0 {
		data := aeEvents[0].Data.(core.ActionEndData)
		t.Logf("  ActionEnd: Total=%d, Success=%d, Failed=%d", data.TotalTools, data.SuccessCount, data.FailedCount)
		if data.FailedCount != 1 {
			t.Errorf("Expected FailedCount=1, got %d", data.FailedCount)
		}
	}

	// Should complete in 2 iterations: tool error → answer
	if result.TotalIterations != 2 {
		t.Errorf("Expected 2 iterations (error + answer), got %d", result.TotalIterations)
	}
}

// intErrorTool simulates a tool that always fails at runtime.
type intErrorTool struct {
	name string
}

func (t *intErrorTool) Info() *core.ToolInfo {
	return &core.ToolInfo{
		Name:          t.name,
		Description:   fmt.Sprintf("Error-prone tool %s", t.name),
		Parameters:    []core.Parameter{{Name: "path", Type: "string", Required: true}},
		SecurityLevel: core.LevelSafe,
		IsReadOnly:    true,
	}
}

func (t *intErrorTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	return nil, errors.New("simulated tool error: file not found or permission denied")
}

// ============================================================================
// SCENARIO I: Empty ToolCalls — DecisionAct With No Tools Falls Back to Answer
// 场景：LLM 返回 decision=act 但没有 tool_calls
// 验证：系统不崩溃、回退到 answer、没有工具执行事件
// ============================================================================

func TestIntegration_EmptyToolCalls_Fallback(t *testing.T) {
	userQuestion := "跟我打个招呼"

	r, _, collector := newIntTestReactor(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		return &gochatcore.Response{
			Content: `{"decision":"act","reasoning":"User just wants a greeting, no tools needed","final_answer":"你好！今天有什么可以帮你的？"}`,
		}, nil
	})

	result, err := r.Run(context.Background(), userQuestion, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	_ = collector.drain(3 * time.Second)
	collector.close()

	t.Logf("=== SCENARIO I: Empty ToolCalls Fallback ===")
	t.Logf("User question: 打招呼")
	t.Logf("Iterations: %d, Answer: %s", result.TotalIterations, result.Answer)

	// No tool events should be emitted since ToolCalls is empty
	tsCount := collector.countByType(core.ToolExecStart)
	if tsCount != 0 {
		t.Errorf("Expected 0 ToolExecStart (empty ToolCalls → answer fallback), got %d", tsCount)
	}

	// Should complete in 1 iteration with an answer
	if result.TotalIterations != 1 {
		t.Errorf("Expected 1 iteration (direct answer fallback), got %d", result.TotalIterations)
	}
	if result.Answer == "" {
		t.Error("Answer should not be empty after empty ToolCalls fallback")
	}
	if !strings.Contains(result.Answer, "你好") {
		t.Error("Answer should contain the greeting from FinalAnswer")
	}
}

type allowStagingOpsChecker struct{}

func (h *allowStagingOpsChecker) CheckPermissions(ctx *core.ToolUseContext) core.PermissionResult {
	return core.PermissionResult{Behavior: core.PermissionAllow}
}

// ============================================================================
// SCENARIO D: Clarify Flow — Ambiguous Input Then Continue
// 场景：用户说"登录功能有问题"太模糊→澄清→用户选B（密码错误）→根因修复
// 验证：两轮Run都能完成、第二轮答案包含密码相关内容、有工具执行事件
// ============================================================================

func TestIntegration_Clarify_AmbiguousInputThenContinue(t *testing.T) {
	ambiguousInput := "登录功能有问题，帮我修一下"

	round := 0
	r, _, collector := newIntTestReactor(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		round++
		if round == 1 {
			return &gochatcore.Response{Content: "{\"decision\":\"clarify\",\"clarification_question\":\"您说'登录有问题'需要更多细节：A)页面打不开/白屏 B)正确密码提示错误 C)登录后立即被踢出 D)手机号收不到验证码。请选字母或补充信息。\"}"}, nil
		}
		if round == 2 {
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{
					{Name: "read_file", Arguments: "{\"path\":\"/src/services/auth/login.go\"}"},
					{Name: "bash", Arguments: "{\"cmd\":\"git log --oneline -10 -- src/services/auth/\"}"},
				},
			}}, nil
		}
		answerE := "{\"decision\":\"answer\",\"final_answer\":\"## 登录问题根因\\n\\n选项B确认：正确密码但提示错误。\\n\\n根因：src/services/auth/login.go:142 密码哈希比较使用了==而非ConstantTimeCompare，且编码不一致(hex vs raw bytes)。\\n\\n修复：使用crypto/subtle.ConstantTimeCompare并统一编码格式。\\n验证：dev环境测试账号登录，检查无password mismatch日志，10次压测session稳定。\"}"
		return &gochatcore.Response{Content: answerE}, nil
	}, &intMockTool{name: "read_file"}, &intMockTool{name: "bash"})

	result1, err := r.Run(context.Background(), ambiguousInput, nil)
	if err != nil {
		t.Fatalf("Round1 failed: %v", err)
	}
	_ = collector.drain(2 * time.Second)

	t.Logf("=== SCENARIO D: Clarify Ambiguous Input Then Continue ===")
	t.Logf("Round1 input: '登录功能有问题，帮我修一下' (too vague)")
	t.Logf("Round1 iterations: %d, reason: %s", result1.TotalIterations, result1.TerminationReason)

	clarifiedInput := "选B，输入正确的管理员密码admin/Admin@2024!但一直提示用户名或密码错误，这个问题是从上周三升级认证服务版本后才出现的。"
	result2, err := r.Run(context.Background(), clarifiedInput, nil)
	if err != nil {
		t.Fatalf("Round2 failed: %v", err)
	}
	_ = collector.drain(3 * time.Second)
	collector.close()

	t.Logf("Round2 input: 选B+详细描述（密码正确但提示错误+升级后出现）")
	t.Logf("Round2 iterations: %d, answer len: %d", result2.TotalIterations, len(result2.Answer))

	if len(result2.Answer) < 50 {
		t.Errorf("After clarification, answer should be substantive, got %d chars", len(result2.Answer))
	}
	if !strings.Contains(result2.Answer, "密码") && !strings.Contains(result2.Answer, "password") {
		t.Error("Answer should address password authentication issue")
	}
	if !strings.Contains(result2.Answer, "ConstantTimeCompare") {
		t.Error("Answer should include the root cause fix (ConstantTimeCompare)")
	}
	tsCount := collector.countByType(core.ToolExecStart)
	t.Logf("Total ToolExecStart across both runs: %d", tsCount)
	if tsCount < 2 {
		t.Errorf("expected >=2 tool executions after clarification, got %d", tsCount)
	}
}

// ============================================================================
// SCENARIO E: Multi-Agent Delegation — Parent Spawns Child Tasks
// 场景：SaaS平台全面安全审计（依赖CVE/认证授权/API注入/基础设施4个子任务）
// 验证：父Agent发起多次delegate调用、每次ActionStart包含delegate工具名、最终聚合报告
// ============================================================================

func TestIntegration_Delegate_MultiAgentSecurityAudit(t *testing.T) {
	userQuestion := "对我们的SaaS平台做全面安全审计：1)依赖库漏洞(CVE) 2)认证授权安全(OAuth2/JWT) 3)API注入防护(SQL/XSS/CSRF) 4)基础设施安全(TLS/K8s/VPC)。每个方面给出CVE编号、风险等级、修复建议，输出汇总风险评估报告。"

	parentIter := 0
	r, _, collector := newIntTestReactor(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		parentIter++
		switch parentIter {
		case 1:
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "delegate", Arguments: "{\"task_id\":\"child-depscan\",\"agent_name\":\"security-scanner\",\"description\":\"Scan package.json/requirements.txt/pom.xml for CVEs\"}"}},
			}}, nil
		case 2:
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "delegate", Arguments: "{\"task_id\":\"child-codeaudit\",\"agent_name\":\"code-reviewer\",\"description\":\"Review auth middleware and JWT/OAuth2 flows\"}"}},
			}}, nil
		case 3:
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "delegate", Arguments: "{\"task_id\":\"child-infrascan\",\"agent_name\":\"infra-auditor\",\"description\":\"Audit TLS certs, K8s RBAC, VPC ACLs, WAF rules\"}"}},
			}}, nil
		case 4:
			return &gochatcore.Response{Content: "{\"decision\":\"answer\",\"final_answer\":\"# SaaS Platform Security Audit Report\\n\\nOverall Risk: MEDIUM-HIGH\\n\\n## 1. Dependency Vulnerabilities\\nFound 47 issues (3 CRITICAL, 5 HIGH). Top: CVE-2024-1234 lodash.prototype.polluted, CVE-2024-5678 express-jwt weak key entropy.\\n\\n## 2. Auth Security\\nFound 27 issues (1 CRITICAL, 3 HIGH). Session fixation risk, JWT none algorithm accepted.\\n\\n## 3. Infra Security\\nFound 19 issues (0 CRITICAL, 2 HIGH). TLS 1.0 enabled on ingress, missing CSP headers.\\n\\n## Summary Matrix\\n| Category | Critical | High | Medium | Low | Total |\\n|----------|----------|------|--------|-----|-------|\\n| Deps      | 2        | 5    | 12     | 28  | 47    |\\n| Code     | 1        | 3    | 8      | 15  | 27    |\\n| Infra     | 0        | 2    | 6      | 11  | 19    |\\n| Total    | 3        | 10   | 26     | 54  | 93    |\\n\\nTop 5 Fixes:\\n1. Upgrade lodash to v4.17.21\\n2. Switch JWT to RS256\\n3. Regenerate session ID after login\\n4. Disable TLSv1.0/1.1 on K8s ingress\\n5. Add Content-Security-Policy headers\"}"}, nil
		default:
			return &gochatcore.Response{Content: "{\"decision\":\"answer\",\"final_answer\":\"Done.\"}"}, nil
		}
	}, &intMockTool{name: "delegate"})

	result, err := r.Run(context.Background(), userQuestion, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	_ = collector.drain(3 * time.Second)
	collector.close()

	t.Logf("=== SCENARIO E: Multi-Agent Delegation Security Audit ===")
	t.Logf("User question: SaaS平台4维度安全审计（依赖/认证/注入/基础设施）")
	t.Logf("Parent iterations: %d, reported: %d, answer len: %d", parentIter, result.TotalIterations, len(result.Answer))

	if result.TotalIterations < 3 {
		t.Errorf("expected >=3 iterations (3 delegate + answer), got %d", result.TotalIterations)
	}
	asCount := collector.countByType(core.ActionStart)
	tsCount := collector.countByType(core.ToolExecStart)
	t.Logf("ActionStart=%d, ToolExecStart=%d", asCount, tsCount)
	if asCount != 3 {
		t.Errorf("expected 3 ActionStart (one per delegation iteration), got %d", asCount)
	}
	if tsCount != 3 {
		t.Errorf("expected 3 ToolExecStart (one delegate call each), got %d", tsCount)
	}
	if !strings.Contains(result.Answer, "Summary") || !strings.Contains(result.Answer, "Total") {
		t.Error("Final answer should contain aggregated summary from child audits")
	}

	actionStarts := collector.getByType(core.ActionStart)
	for i, as := range actionStarts {
		data := as.Data.(core.ActionStartData)
		t.Logf("  Delegate[%d]: Iter=%d, Tools=%d, Names=%v", i, data.Iteration, data.ToolCount, data.ToolNames)
		if data.ToolNames[0] != "delegate" {
			t.Errorf("Delegate[%d]: expected tool='delegate', got %v", i, data.ToolNames)
		}
		if data.ToolCount != 1 {
			t.Errorf("Delegate[%d]: expected 1 tool per delegation, got %d", i, data.ToolCount)
		}
	}
}

// ============================================================================
// SCENARIO F: Mixed Decision Path — Tools Then Answer With Rich Content
// 场景：复杂的Kubernetes集群故障排查（多轮工具调用+结构化最终回答）
// 验证：混合路径下事件流完整、最终答案包含结构化数据、迭代计数准确
// ============================================================================

func TestIntegration_MixedPath_KubernetesTroubleshooting(t *testing.T) {
	userQuestion := "我们K8s集群的ingress-nginx控制器频繁重启，已经重启了12次在过去2小时。同时观察到：1)node-exporter在某些节点上报disk pressure 2)coredns解析延迟偶尔超过500ms 3)prometheus的TSDB WAL目录占用磁盘90%。请排查这些问题的关联性并给出修复优先级排序。"

	iterCount := 0
	r, _, collector := newIntTestReactor(func(ctx context.Context, input reactor.CallInput) (*gochatcore.Response, error) {
		iterCount++
		switch iterCount {
		case 1:
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "bash", Arguments: "{\"cmd\":\"kubectl get pods -n ingress-nginx -o wide\"}"}},
			}}, nil
		case 2:
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{
					{Name: "bash", Arguments: "{\"cmd\":\"kubectl describe node worker-03 | grep -A5 Conditions\"}"},
					{Name: "bash", Arguments: "{\"cmd\":\"kubectl top nodes\"}"},
				},
			}}, nil
		case 3:
			return &gochatcore.Response{Message: gochatcore.Message{
				ToolCalls: []gochatcore.ToolCall{{Name: "bash", Arguments: "{\"cmd\":\"kubectl exec -n monitoring prometheus-0 -- du -sh /prometheus/wal\"}"}},
			}}, nil
		default:
			answerF := "{\"decision\":\"answer\",\"final_answer\":\"# K8s集群故障关联性分析与修复优先级\\n\\n## 问题关联链\\ningress-nginx OOMKill → 重启风暴 → DNS缓存失效 → coredns延迟↑ → prometheus抓取失败 → WAL堆积 → disk pressure\\n\\n## 修复优先级\\nP0 (立即): 扩容ingress-nginx memory limit 512Mi→2Gi\\nP1 (1h内): 清理prometheus WAL (wal-compression + compact)\\nP2 (今日): 增加node disk capacity或设置 eviction soft threshold\\nP3 (本周): coredns autoscaling + local cache tuning\\n\\n## 根因\\ningress-nginx内存泄漏(v1.9.3 known bug #12345)，每次请求泄露~4KB，在高QPS下约2小时触OOM。\"}"
			return &gochatcore.Response{Content: answerF}, nil
		}
	}, &intMockTool{name: "bash"}, &intMockTool{name: "read_file"})

	result, err := r.Run(context.Background(), userQuestion, nil)
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	_ = collector.drain(3 * time.Second)
	collector.close()

	t.Logf("=== SCENARIO F: K8s Troubleshooting Mixed Path ===")
	t.Logf("User question: ingress-nginx重启12次+disk pressure+coredns延迟+WAL 90%%")
	t.Logf("Iterations: %d, Answer length: %d", result.TotalIterations, len(result.Answer))

	if result.TotalIterations < 4 {
		t.Errorf("expected >=4 iterations (3 tool rounds + 1 answer), got %d", result.TotalIterations)
	}
	asCount := collector.countByType(core.ActionStart)
	tsCount := collector.countByType(core.ToolExecStart)
	teCount := collector.countByType(core.ToolExecEnd)
	t.Logf("ActionStart=%d, ToolExecStart=%d, ToolExecEnd=%d", asCount, tsCount, teCount)
	if asCount < 3 {
		t.Errorf("expected >=3 ActionStart, got %d", asCount)
	}
	if tsCount != teCount {
		t.Errorf("ToolExecStart(%d) != ToolExecEnd(%d)", tsCount, teCount)
	}
	if !strings.Contains(result.Answer, "优先级") && !strings.Contains(result.Answer, "priority") {
		t.Error("Answer should contain priority/ranked fixes")
	}
	if !strings.Contains(result.Answer, "ingress") {
		t.Error("Answer should mention ingress-nginx root cause")
	}

	toolEvents := collector.getByType(core.ToolExecStart)
	for i, ev := range toolEvents {
		data := ev.Data.(core.ToolExecStartData)
		t.Logf("  ToolExecStart[%d]: Name=%s, HasParams=%v", i, data.ToolName, len(data.Params) > 0)
	}
}
