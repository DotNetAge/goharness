package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
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

// mockSession creates a simple session for testing.
func mockSession() *session.Session {
	store := newMockSessionStore()
	sess, err := session.New("test-agent", "", "/tmp/test", store, logging.NewNopLogger())
	if err != nil {
		panic(err)
	}
	return sess
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
		result, err := executor.Execute(ctxWithLogger(), "TestExecute", map[string]any{"key": "value"})
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
		_, err := executor.Execute(ctxWithLogger(), "NonExistentTool", nil)
		if err == nil {
			t.Error("不存在的工具应返回错误")
		}
	})

	t.Run("空参数执行", func(t *testing.T) {
		result, err := executor.Execute(ctxWithLogger(), "TestExecute", nil)
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
		ctx, cancel := context.WithTimeout(ctxWithLogger(), 100*time.Millisecond)
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
		result, err := executor.Execute(ctxWithLogger(), "ErrorTool", nil)

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
		result, err := executor.Execute(ctxWithLogger(), "MapResultTool", nil)

		if err != nil {
			t.Fatalf("Execute 失败: %v", err)
		}
		if result.Result == "" {
			t.Error("map 结果应被序列化为 JSON 字符串")
		}
	})
}

// TestToolExecutor_ExtractsReadImages 验证执行 Read 工具返回 *ReadResult 时，
// 图片数据（ReadResult.Images）被提取到 ToolExecutionResult.Images，
// 而不会因 json.Marshal（Images 带 json:"-" 标签）被静默丢弃。
func TestToolExecutor_ExtractsReadImages(t *testing.T) {
	registry := NewDefaultToolRegistry()
	imgTool := &mockExecutorTool{
		name: "ReadImageTool",
		result: &ReadResult{
			Data: &ReadData{
				Success: true, Path: "/tmp/a.png", Content: "图片摘要：512x300",
			},
			Images: []ImageContent{
				{MediaType: "image/png", Base64Data: "aGVsbG8=", Width: 512, Height: 300, RawSize: 1000, CompressedSize: 8},
			},
		},
	}
	registry.Register(imgTool)

	executor := NewToolExecutor(registry)
	result, err := executor.Execute(ctxWithLogger(), "ReadImageTool", nil)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if result == nil {
		t.Fatal("结果不应为 nil")
	}
	if len(result.Images) != 1 {
		t.Fatalf("期望提取 1 张图片，得到 %d", len(result.Images))
	}
	if result.Images[0].MediaType != "image/png" || result.Images[0].Base64Data != "aGVsbG8=" {
		t.Errorf("图片数据提取不正确: %+v", result.Images[0])
	}
	// 文本结果仍为正常 JSON 序列化；base64 图片数据不应混入文本
	if !strings.Contains(result.Result, "图片摘要") {
		t.Errorf("文本结果应包含摘要内容: %s", result.Result)
	}
	if strings.Contains(result.Result, "aGVsbG8=") {
		t.Error("base64 图片数据不应混入文本结果")
	}
}

// TestToolExecutor_NonReadResultNoImages 验证普通工具结果不携带图片数据。
func TestToolExecutor_NonReadResultNoImages(t *testing.T) {
	registry := NewDefaultToolRegistry()
	plain := &mockExecutorTool{name: "PlainTool", result: "普通文本结果"}
	registry.Register(plain)

	executor := NewToolExecutor(registry)
	result, err := executor.Execute(ctxWithLogger(), "PlainTool", nil)
	if err != nil {
		t.Fatalf("Execute 失败: %v", err)
	}
	if len(result.Images) != 0 {
		t.Errorf("普通工具结果不应携带图片: %d", len(result.Images))
	}
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
	result, err := executor.Execute(ctxWithLogger(), "LargeResultTool", nil)

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
		sess := mockSession()
		opt := WithSession(sess)
		cfg := &executorConfig{}
		opt(cfg)
		if cfg.session != sess {
			t.Errorf("session 设置失败: 期望 %v，得到 %v", sess, cfg.session)
		}
	})

	t.Run("组合多个选项", func(t *testing.T) {
		cfg := &executorConfig{}
		sess := mockSession()
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

// TestEnhanceError 验证执行器错误引导增强（enhanceError）：
//  1. 已是引导格式的错误 → 原样透传，不做二次包装
//  2. 文件不存在（os.ErrNotExist）→ 走 enhanceFileError 相似路径建议
//  3. 其它错误 → 兜底引导（含「下一步我应该」，且保留 %w 链）
func TestEnhanceError(t *testing.T) {
	t.Run("引导格式错误透传", func(t *testing.T) {
		guided := fmt.Errorf("%s", GuideMissingParam("TestTool", "key"))
		got := enhanceError(guided, "TestTool", "")
		if got.Error() != guided.Error() {
			t.Errorf("已引导错误应原样透传，got=%v want=%v", got, guided)
		}
	})

	t.Run("文件不存在走增强", func(t *testing.T) {
		orig := &os.PathError{Op: "open", Path: "/nonexistent_dir_enhance/foo.txt", Err: os.ErrNotExist}
		got := enhanceError(orig, "Read", "/nonexistent_dir_enhance/foo.txt")
		if !strings.Contains(got.Error(), "下一步我应该") {
			t.Errorf("ENOENT 应带引导建议，got=%v", got)
		}
		if !errors.Is(got, os.ErrNotExist) {
			t.Errorf("应保留 os.ErrNotExist 错误链，got=%v", got)
		}
	})

	t.Run("未知错误兜底零包装", func(t *testing.T) {
		orig := errors.New("底层未知错误")
		got := enhanceError(orig, "SomeTool", "")
		if got != orig {
			t.Errorf("未知错误应原样返回（不添加任何指引），got=%v want=%v", got, orig)
		}
		if strings.Contains(got.Error(), "下一步我应该") {
			t.Errorf("兜底不应包含指引话术，got=%v", got)
		}
	})
}
