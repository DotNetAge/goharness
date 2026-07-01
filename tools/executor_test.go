package tools

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/session"
)

// mockExecutorTool is a mock tool for testing the executor.
type mockExecutorTool struct {
	name          string
	result        any
	err           error
	execDelay     time.Duration
	executeFunc   func(ctx context.Context, params map[string]any) (any, error)
	maxResultSize int
}

func (m *mockExecutorTool) Info() *ToolInfo {
	info := &ToolInfo{
		Name:          m.name,
		Description:   fmt.Sprintf("Mock tool: %s", m.name),
		SecurityLevel: events.LevelSafe,
	}
	if m.maxResultSize > 0 {
		info.MaxResultSizeChars = m.maxResultSize
	}
	return info
}

func (m *mockExecutorTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	if m.execDelay > 0 {
		select {
		case <-time.After(m.execDelay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	if m.executeFunc != nil {
		return m.executeFunc(ctx, params)
	}

	if m.err != nil {
		return nil, m.err
	}
	return m.result, nil
}

// mockPermissionChecker is a mock permission checker.
type mockPermissionChecker struct {
	behavior PermissionBehavior
	message  string
}

func (m *mockPermissionChecker) CheckPermissions(ctx *ToolUseContext) PermissionResult {
	return PermissionResult{
		Behavior: m.behavior,
		Message:  m.message,
	}
}

// mockSession creates a simple session for testing.
func mockSession() *session.Session {
	return session.NewSession("test-session", "test-agent")
}

// TestNewToolExecutor tests executor creation.
func TestNewToolExecutor(t *testing.T) {
	t.Run("基本创建", func(t *testing.T) {
		registry := NewDefaultToolRegistry()
		executor := NewToolExecutor(registry)

		if executor == nil {
			t.Fatal("执行器不应为 nil")
		}
	})

	t.Run("带选项创建", func(t *testing.T) {
		registry := NewDefaultToolRegistry()
		executor := NewToolExecutor(
			registry,
			WithSession(mockSession()),
		)

		if executor == nil {
			t.Fatal("带选项创建不应返回 nil")
		}
	})
}

// TestToolExecutor_Execute_Basic tests basic execution.
func TestToolExecutor_Execute_Basic(t *testing.T) {
	registry := NewDefaultToolRegistry()
	testTool := &mockExecutorTool{
		name:   "TestExecute",
		result: "execution success",
	}
	registry.Register(testTool)

	executor := NewToolExecutor(registry)

	t.Run("成功执行", func(t *testing.T) {
		result, err := executor.Execute(context.Background(), "TestExecute", map[string]any{"key": "value"})
		if err != nil {
			t.Fatalf("Execute 返回错误: %v", err)
		}
		if result == nil {
			t.Fatal("结果不应为 nil")
		}
		if result.Result != "execution success" {
			t.Errorf("结果不匹配: 期望 'execution success'，得到 '%s'", result.Result)
		}
		if result.ToolName != "TestExecute" {
			t.Errorf("ToolName 不匹配: 期望 'TestExecute'，得到 '%s'", result.ToolName)
		}
		if result.Duration <= 0 {
			t.Error("Duration 应大于 0")
		}
	})

	t.Run("工具不存在", func(t *testing.T) {
		_, err := executor.Execute(context.Background(), "NonExistentTool", nil)
		if err == nil {
			t.Error("不存在的工具应返回错误")
		}
	})

	t.Run("空参数执行", func(t *testing.T) {
		result, err := executor.Execute(context.Background(), "TestExecute", nil)
		if err != nil {
			t.Fatalf("nil 参数应能正常执行: %v", err)
		}
		if result.Error != nil {
			t.Errorf("不应有错误: %v", result.Error)
		}
	})
}

// TestToolExecutor_Execute_Timeout tests timeout handling.
func TestToolExecutor_Execute_Timeout(t *testing.T) {
	registry := NewDefaultToolRegistry()

	slowTool := &mockExecutorTool{
		name:      "SlowTool",
		result:    "slow result",
		execDelay: 5 * time.Second,
	}
	registry.Register(slowTool)

	executor := NewToolExecutor(registry)

	t.Run("上下文超时取消", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()

		result, err := executor.Execute(ctx, "SlowTool", nil)
		if err != nil {
			t.Fatalf("超时不应返回 error（错误在 Result 中）: %v", err)
		}
		if result == nil {
			t.Fatal("结果不应为 nil")
		}
		if result.Error == nil {
			t.Error("超时应设置 Error 字段")
		}
	})
}

// TestToolExecutor_Execute_ErrorPropagation tests error propagation.
func TestToolExecutor_Execute_ErrorPropagation(t *testing.T) {
	registry := NewDefaultToolRegistry()

	t.Run("工具返回错误", func(t *testing.T) {
		errorTool := &mockExecutorTool{
			name: "ErrorTool",
			err:  errors.New("tool execution failed"),
		}
		registry.Register(errorTool)

		executor := NewToolExecutor(registry)
		result, err := executor.Execute(context.Background(), "ErrorTool", nil)

		if err != nil {
			t.Fatalf("工具错误应在 Result 中，不应直接返回: %v", err)
		}
		if result.Error == nil {
			t.Error("Result 应包含错误信息")
		}
	})

	t.Run("工具返回非字符串结果", func(t *testing.T) {
		mapTool := &mockExecutorTool{
			name:   "MapResultTool",
			result: map[string]any{"key": "value", "num": 42},
		}
		registry.Register(mapTool)

		executor := NewToolExecutor(registry)
		result, err := executor.Execute(context.Background(), "MapResultTool", nil)

		if err != nil {
			t.Fatalf("Execute 失败: %v", err)
		}
		if result.Result == "" {
			t.Error("map 结果应被序列化为 JSON 字符串")
		}
	})
}

// TestToolExecutor_Permission tests permission checking.
func TestToolExecutor_Permission(t *testing.T) {
	registry := NewDefaultToolRegistry()
	testTool := &mockExecutorTool{name: "PermTool", result: "ok"}
	registry.Register(testTool)

	t.Run("权限允许", func(t *testing.T) {
		checker := &mockPermissionChecker{behavior: PermissionAllow, message: "allowed"}
		executor := NewToolExecutor(registry, WithPermissionChecker(checker))

		result, err := executor.Execute(context.Background(), "PermTool", nil)
		if err != nil {
			t.Fatalf("权限允许时执行失败: %v", err)
		}
		if result.Error != nil {
			t.Errorf("权限允许时不应有错误: %v", result.Error)
		}
	})

	t.Run("权限拒绝", func(t *testing.T) {
		checker := &mockPermissionChecker{behavior: PermissionDeny, message: "not allowed"}
		executor := NewToolExecutor(registry, WithPermissionChecker(checker))

		result, err := executor.Execute(context.Background(), "PermTool", nil)
		if err != nil {
			t.Fatalf("权限拒绝时不应返回 error: %v", err)
		}
		if result.Error == nil {
			t.Error("权限拒绝时应有错误信息")
		}
	})
}

// TestToolExecutor_ResetCycle tests ResetCycle doesn't panic.
func TestToolExecutor_ResetCycle(t *testing.T) {
	registry := NewDefaultToolRegistry()
	executor := NewToolExecutor(registry)

	t.Run("ResetCycle 不 panic", func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("ResetCycle 不应 panic: %v", r)
			}
		}()
		executor.ResetCycle()
	})
}

// TestToolExecutionResult validates the ToolExecutionResult struct.
func TestToolExecutionResult(t *testing.T) {
	t.Run("成功结果", func(t *testing.T) {
		result := &ToolExecutionResult{
			Result:   "test content",
			Duration: 100 * time.Millisecond,
			ToolName: "TestTool",
		}

		if result.Result != "test content" {
			t.Error("Result 字段不正确")
		}
		if result.Duration != 100*time.Millisecond {
			t.Error("Duration 字段不正确")
		}
		if result.ToolName != "TestTool" {
			t.Error("ToolName 字段不正确")
		}
		if result.Error != nil {
			t.Error("成功结果的 Error 应为 nil")
		}
	})

	t.Run("失败结果", func(t *testing.T) {
		result := &ToolExecutionResult{
			Error:    fmt.Errorf("something went wrong"),
			ToolName: "FailingTool",
		}

		if result.Error == nil {
			t.Error("失败结果的 Error 不应为 nil")
		}
		if result.Result != "" {
			t.Error("失败结果的 Result 应为空")
		}
	})
}

// TestToolExecutor_ConcurrentExecution tests concurrent execution.
func TestToolExecutor_ConcurrentExecution(t *testing.T) {
	registry := NewDefaultToolRegistry()

	for i := 0; i < 10; i++ {
		tool := &mockExecutorTool{
			name:   fmt.Sprintf("ConcurrentTool%d", i),
			result: fmt.Sprintf("result-%d", i),
		}
		registry.Register(tool)
	}

	executor := NewToolExecutor(registry)
	ctx := context.Background()

	done := make(chan bool, 10)
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		go func(idx int) {
			defer func() { done <- true }()
			name := fmt.Sprintf("ConcurrentTool%d", idx)
			_, err := executor.Execute(ctx, name, map[string]any{"idx": idx})
			if err != nil {
				errors <- err
			}
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}

	select {
	case err := <-errors:
		t.Errorf("并发执行中发生错误: %v", err)
	default:
	}
}

// TestToolExecutor_ContextCancellation tests context cancellation.
func TestToolExecutor_ContextCancellation(t *testing.T) {
	registry := NewDefaultToolRegistry()

	blockingTool := &mockExecutorTool{
		name: "BlockingTool",
		executeFunc: func(ctx context.Context, params map[string]any) (any, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(10 * time.Second):
				return "completed", nil
			}
		},
	}
	registry.Register(blockingTool)

	executor := NewToolExecutor(registry)

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	result, err := executor.Execute(ctx, "BlockingTool", nil)
	if err != nil {
		t.Fatalf("上下文取消时 Execute 不应返回 error: %v", err)
	}
	if result == nil {
		t.Fatal("结果不应为 nil")
	}
	if result.Error == nil {
		t.Error("上下文取消时应有错误信息")
	}
}

// TestToolExecutor_LargeResult tests large result truncation.
func TestToolExecutor_LargeResult(t *testing.T) {
	registry := NewDefaultToolRegistry()

	largeContent := make([]byte, 50000)
	for i := range largeContent {
		largeContent[i] = 'x'
	}

	largeTool := &mockExecutorTool{
		name:          "LargeResultTool",
		result:        string(largeContent),
		maxResultSize: 25000,
	}
	registry.Register(largeTool)

	executor := NewToolExecutor(registry)
	result, err := executor.Execute(context.Background(), "LargeResultTool", nil)

	if err != nil {
		t.Fatalf("大结果执行失败: %v", err)
	}
	if len(result.Result) >= 50000 {
		t.Errorf("结果应被截断: 得到 %d 字符", len(result.Result))
	}
}

// TestExecutorOptionFunctions tests executor option functions.
func TestExecutorOptionFunctions(t *testing.T) {
	t.Run("WithSession", func(t *testing.T) {
		sess := session.NewSession("test-session", "test-agent")
		opt := WithSession(sess)
		cfg := &executorConfig{}
		opt(cfg)
		if cfg.session != sess {
			t.Errorf("session 设置失败: 期望 %v，得到 %v", sess, cfg.session)
		}
	})

	t.Run("组合多个选项", func(t *testing.T) {
		cfg := &executorConfig{}
		sess := session.NewSession("test-session", "test-agent")
		opts := []ExecutorOption{
			WithSession(sess),
			WithEventEmitter(func(events.ReactEvent) {}),
		}
		for _, opt := range opts {
			opt(cfg)
		}

		if cfg.session != sess {
			t.Error("组合选项中的 session 设置失败")
		}
		if cfg.eventEmitter == nil {
			t.Error("组合选项中的 eventEmitter 设置失败")
		}
	})
}
