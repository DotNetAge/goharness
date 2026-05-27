package tools

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/DotNetAge/goreact/events"
)

// mockTool 是用于测试的 FuncTool 模拟实现
type mockTool struct {
	info     *ToolInfo
	execFunc func(ctx context.Context, params map[string]any) (any, error)
}

func (m *mockTool) Info() *ToolInfo {
	return m.info
}

func (m *mockTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	if m.execFunc != nil {
		return m.execFunc(ctx, params)
	}
	return "mock result", nil
}

// TestDefaultToolRegistry_Register 测试工具注册功能
func TestDefaultToolRegistry_Register(t *testing.T) {
	registry := NewDefaultToolRegistry()

	t.Run("注册单个工具", func(t *testing.T) {
		tool := &mockTool{
			info: &ToolInfo{Name: "TestTool", Description: "测试工具"},
		}
		err := registry.Register(tool)
		if err != nil {
			t.Fatalf("注册工具失败: %v", err)
		}

		// 验证工具可以被获取
		got, ok := registry.Get("TestTool")
		if !ok {
			t.Fatal("无法获取刚注册的工具")
		}
		if got.Info().Name != "TestTool" {
			t.Errorf("期望工具名 'TestTool'，得到 '%s'", got.Info().Name)
		}
	})

	t.Run("重复注册应返回错误", func(t *testing.T) {
		tool1 := &mockTool{info: &ToolInfo{Name: "DupTool", Description: "第一个"}}
		tool2 := &mockTool{info: &ToolInfo{Name: "DupTool", Description: "第二个"}}

		err := registry.Register(tool1)
		if err != nil {
			t.Fatalf("首次注册失败: %v", err)
		}

		err = registry.Register(tool2)
		if err == nil {
			t.Error("期望重复注册返回错误")
		}
	})

	t.Run("注册多个不同工具", func(t *testing.T) {
		newReg := NewDefaultToolRegistry()
		tools := []FuncTool{
			&mockTool{info: &ToolInfo{Name: "ToolA", Description: "A"}},
			&mockTool{info: &ToolInfo{Name: "ToolB", Description: "B"}},
			&mockTool{info: &ToolInfo{Name: "ToolC", Description: "C"}},
		}

		for _, tool := range tools {
			if err := newReg.Register(tool); err != nil {
				t.Fatalf("注册工具 %s 失败: %v", tool.Info().Name, err)
			}
		}

		all := newReg.All()
		if len(all) != 3 {
			t.Errorf("期望 3 个工具，得到 %d 个", len(all))
		}
	})
}

// TestDefaultToolRegistry_Get 测试工具查找功能
func TestDefaultToolRegistry_Get(t *testing.T) {
	registry := NewDefaultToolRegistry()

	t.Run("获取存在的工具", func(t *testing.T) {
		registry.Register(&mockTool{info: &ToolInfo{Name: "ExistsTool"}})

		tool, ok := registry.Get("ExistsTool")
		if !ok {
			t.Fatal("应该能找到已注册的工具")
		}
		if tool == nil {
			t.Error("返回的工具不应为 nil")
		}
	})

	t.Run("获取不存在的工具", func(t *testing.T) {
		tool, ok := registry.Get("NonExistentTool")
		if ok {
			t.Error("不应该找到不存在的工具")
		}
		if tool != nil {
			t.Error("不存在的工具应返回 nil")
		}
	})

	t.Run("空名称查找", func(t *testing.T) {
		tool, ok := registry.Get("")
		if ok {
			t.Error("空名称不应匹配任何工具")
		}
		if tool != nil {
			t.Error("空名称查找应返回 nil")
		}
	})
}

// TestDefaultToolRegistry_All 测试获取所有工具
func TestDefaultToolRegistry_All(t *testing.T) {
	t.Run("空注册表返回空切片", func(t *testing.T) {
		registry := NewDefaultToolRegistry()
		all := registry.All()

		if all == nil {
			t.Error("All() 不应返回 nil，应返回空切片")
		}
		if len(all) != 0 {
			t.Errorf("空注册表应返回 0 个工具，得到 %d", len(all))
		}
	})

	t.Run("非空注册表返回所有工具", func(t *testing.T) {
		registry := NewDefaultToolRegistry()
		count := 5
		for i := 0; i < count; i++ {
			registry.Register(&mockTool{
				info: &ToolInfo{
					Name:        fmt.Sprintf("Tool%d", i),
					Description: fmt.Sprintf("工具 %d", i),
				},
			})
		}

		all := registry.All()
		if len(all) != count {
			t.Errorf("期望 %d 个工具，得到 %d", count, len(all))
		}
	})
}

// TestDefaultToolRegistry_Names 测试获取所有工具名称（通过 All + Info）
func TestDefaultToolRegistry_Names(t *testing.T) {
	registry := NewDefaultToolRegistry()
	expectedNames := []string{"Alpha", "Beta", "Gamma"}

	for _, name := range expectedNames {
		registry.Register(&mockTool{info: &ToolInfo{Name: name}})
	}

	all := registry.All()
	names := make([]string, len(all))
	for i, tool := range all {
		names[i] = tool.Info().Name
	}

	if len(names) != len(expectedNames) {
		t.Errorf("工具数量不匹配：期望 %d，得到 %d", len(expectedNames), len(names))
	}

	nameSet := make(map[string]bool)
	for _, n := range expectedNames {
		nameSet[n] = true
	}
	for _, n := range names {
		if !nameSet[n] {
			t.Errorf("意外的工具名: %s", n)
		}
	}
}

// TestDefaultToolRegistry_Remove 测试工具移除功能
func TestDefaultToolRegistry_Remove(t *testing.T) {
	registry := NewDefaultToolRegistry()

	t.Run("移除存在的工具", func(t *testing.T) {
		registry.Register(&mockTool{info: &ToolInfo{Name: "ToRemove"}})

		err := registry.Remove("ToRemove")
		if err != nil {
			t.Fatalf("移除工具失败: %v", err)
		}

		_, ok := registry.Get("ToRemove")
		if ok {
			t.Error("移除后不应能找到工具")
		}
	})

	t.Run("移除不存在的工具应返回错误", func(t *testing.T) {
		err := registry.Remove("NonExistent")
		if err == nil {
			t.Error("移除不存在的工具应返回错误")
		}
	})
}

// TestFuncTool_Interface 测试 FuncTool 接口实现
func TestFuncTool_Interface(t *testing.T) {
	t.Run("模拟工具实现接口", func(t *testing.T) {
		tool := &mockTool{
			info: &ToolInfo{
				Name:               "InterfaceTest",
				Description:        "接口测试工具",
				Tags:               []string{"test", "mock"},
				SecurityLevel:      events.LevelSafe,
				IsIdempotent:       true,
				Parameters:         []Parameter{{Name: "input", Type: "string", Required: true}},
				ReturnType:         "string",
				MaxResultSizeChars: 1000,
			},
		}

		var _ FuncTool = tool // 编译时检查接口实现

		info := tool.Info()
		if info.Name != "InterfaceTest" {
			t.Errorf("Name 不匹配")
		}
		if !info.IsIdempotent {
			t.Error("IsIdempotent 应为 true")
		}
		if len(info.Parameters) != 1 {
			t.Errorf("期望 1 个参数，得到 %d", len(info.Parameters))
		}
	})

	t.Run("Execute 方法正常执行", func(t *testing.T) {
		expectedResult := "execution result"
		tool := &mockTool{
			info: &ToolInfo{Name: "ExecTest"},
			execFunc: func(ctx context.Context, params map[string]any) (any, error) {
				return expectedResult, nil
			},
		}

		result, err := tool.Execute(context.Background(), map[string]any{"key": "value"})
		if err != nil {
			t.Fatalf("Execute 返回错误: %v", err)
		}
		if result != expectedResult {
			t.Errorf("结果不匹配: 期望 '%s'，得到 '%v'", expectedResult, result)
		}
	})

	t.Run("Execute 方法返回错误", func(t *testing.T) {
		expectedErr := "execution failed"
		tool := &mockTool{
			info: &ToolInfo{Name: "ErrorTest"},
			execFunc: func(ctx context.Context, params map[string]any) (any, error) {
				return nil, errors.New(expectedErr)
			},
		}

		result, err := tool.Execute(context.Background(), nil)
		if err == nil {
			t.Fatal("应返回错误")
		}
		if result != nil {
			t.Error("错误情况下 result 应为 nil")
		}
		if err.Error() != expectedErr {
			t.Errorf("错误消息不匹配: 期望 '%s'，得到 '%s'", expectedErr, err.Error())
		}
	})
}

// TestToolInfo_FieldValidation 测试 ToolInfo 字段验证
func TestToolInfo_FieldValidation(t *testing.T) {
	t.Run("完整 ToolInfo 初始化", func(t *testing.T) {
		info := &ToolInfo{
			Name:          "CompleteTool",
			Description:   "完整的工具信息",
			Prompt:        "详细使用说明",
			Tags:          []string{"tag1", "tag2"},
			SecurityLevel: events.LevelSensitive,
			IsIdempotent:  false,
			IsAsync:       true,
			Parameters: []Parameter{
				{Name: "param1", Type: "string", Required: true, Description: "第一个参数"},
				{Name: "param2", Type: "number", Required: false, Default: 42, Description: "第二个参数"},
			},
			ReturnType:         "object",
			Examples:           []string{"example 1", "example 2"},
			MaxResultSizeChars: 50000,
			IsReadOnly:         true,
		}

		if info.Name != "CompleteTool" {
			t.Errorf("Name 不正确")
		}
		if len(info.Tags) != 2 {
			t.Errorf("Tags 数量不正确: 期望 2，得到 %d", len(info.Tags))
		}
		if len(info.Parameters) != 2 {
			t.Errorf("Parameters 数量不正确: 期望 2，得到 %d", len(info.Parameters))
		}
		if info.MaxResultSizeChars != 50000 {
			t.Errorf("MaxResultSizeChars 不正确: 期望 50000，得到 %d", info.MaxResultSizeChars)
		}
		if !info.IsReadOnly {
			t.Error("IsReadOnly 应为 true")
		}
	})

	t.Run("Parameter 字段验证", func(t *testing.T) {
		param := Parameter{
			Name:        "testParam",
			Type:        "boolean",
			Required:    true,
			Default:     false,
			Description: "测试参数",
			Enum:        []any{true, false},
		}

		if param.Name != "testParam" {
			t.Errorf("Name 不正确")
		}
		if param.Type != "boolean" {
			t.Errorf("Type 不正确: 期望 'boolean'，得到 '%s'", param.Type)
		}
		if !param.Required {
			t.Error("Required 应为 true")
		}
		if param.Default != false {
			t.Errorf("Default 不正确: 期望 false，得到 %v", param.Default)
		}
		if len(param.Enum) != 2 {
			t.Errorf("Enum 数量不正确: 期望 2，得到 %d", len(param.Enum))
		}
	})

	t.Run("空值和零值处理", func(t *testing.T) {
		info := &ToolInfo{}

		if info.Name != "" {
			t.Errorf("默认 Name 应为空字符串")
		}
		if info.SecurityLevel != 0 {
			t.Errorf("默认 SecurityLevel 应为 0")
		}
		if info.Parameters == nil {
			info.Parameters = []Parameter{} // 初始化以避免 panic
		}
		if info.Tags == nil {
			info.Tags = []string{} // 初始化以避免 panic
		}
	})
}

// TestDefaultToolRegistry_ConcurrentAccess 测试并发访问安全性
func TestDefaultToolRegistry_ConcurrentAccess(t *testing.T) {
	registry := NewDefaultToolRegistry()

	t.Run("并发注册", func(t *testing.T) {
		done := make(chan bool, 10)

		for i := 0; i < 10; i++ {
			go func(idx int) {
				defer func() { done <- true }()
				tool := &mockTool{
					info: &ToolInfo{Name: fmt.Sprintf("ConcurrentTool%d", idx)},
				}
				_ = registry.Register(tool)
			}(i)
		}

		for i := 0; i < 10; i++ {
			<-done
		}

		all := registry.All()
		if len(all) == 0 {
			t.Error("并发注册后应有工具存在")
		}
	})

	t.Run("并发读写", func(t *testing.T) {
		registry.Register(&mockTool{info: &ToolInfo{Name: "ReadWriteTest"}})

		done := make(chan bool, 20)
		for i := 0; i < 10; i++ {
			go func() {
				defer func() { done <- true }()
				_, _ = registry.Get("ReadWriteTest")
			}()
			go func() {
				defer func() { done <- true }()
				_ = registry.All()
			}()
		}

		for i := 0; i < 20; i++ {
			<-done
		}
	})
}

// TestDefaultToolRegistry_FindAvailable 测试过滤功能
func TestDefaultToolRegistry_FindAvailable(t *testing.T) {
	registry := NewDefaultToolRegistry()

	registry.Register(&mockTool{
		info: &ToolInfo{
			Name:          "SafeRead",
			Description:   "安全读取工具",
			Tags:          []string{"file", "read", "safe"},
			SecurityLevel: events.LevelSafe,
		},
	})
	registry.Register(&mockTool{
		info: &ToolInfo{
			Name:          "RiskyWrite",
			Description:   "风险写入工具",
			Tags:          []string{"file", "write", "risky"},
			SecurityLevel: events.LevelHighRisk,
		},
	})
	registry.Register(&mockTool{
		info: &ToolInfo{
			Name:          "WebSearch",
			Description:   "网络搜索工具",
			Tags:          []string{"web", "search", "safe"},
			SecurityLevel: events.LevelSensitive,
		},
	})

	t.Run("无过滤器返回全部", func(t *testing.T) {
		result := registry.FindAvailable(nil)
		if len(result) != 3 {
			t.Errorf("无过滤器应返回所有工具: 期望 3，得到 %d", len(result))
		}
	})

	t.Run("按安全级别过滤", func(t *testing.T) {
		filter := &ToolFilter{Security: events.LevelHighRisk}
		result := registry.FindAvailable(filter)

		for _, tool := range result {
			if tool.Info().Name == "SafeRead" && tool.Info().SecurityLevel == events.LevelSafe {
				t.Errorf("LevelSafe 工具不应出现在 LevelHighRisk 过滤结果中")
			}
		}
	})

	t.Run("按关键词过滤", func(t *testing.T) {
		filter := &ToolFilter{Keywords: []string{"web"}}
		result := registry.FindAvailable(filter)

		found := false
		for _, tool := range result {
			if tool.Info().Name == "WebSearch" {
				found = true
				break
			}
		}
		if !found {
			t.Error("关键词 'web' 应匹配 WebSearch 工具")
		}
	})

	t.Run("按允许名称列表过滤", func(t *testing.T) {
		filter := &ToolFilter{AllowedNames: []string{"SafeRead"}}
		result := registry.FindAvailable(filter)

		if len(result) != 1 || result[0].Info().Name != "SafeRead" {
			t.Error("AllowedNames 过滤器应只返回 SafeRead")
		}
	})
}
