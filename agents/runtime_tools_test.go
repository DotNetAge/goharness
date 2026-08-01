package agents

import (
	"testing"

	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/tools"
)

// TestReadImageReading_ModelVision 验证默认注册的 Read 工具
// 仅对支持视觉理解的模型（Visioning=true）启用图片读取。
func TestReadImageReading_ModelVision(t *testing.T) {
	t.Run("Visioning=false 时禁用图片读取", func(t *testing.T) {
		rt := newTestRuntime(t, WithModel(config.ModelConfig{Name: "m", Provider: "mock"}))
		tool, ok := rt.ToolRegistry().Get("Read")
		if !ok {
			t.Fatal("Read 工具未注册")
		}
		read, ok := tool.(*tools.Read)
		if !ok {
			t.Fatalf("Read 工具类型断言失败: %T", tool)
		}
		if read.Limits().EnableImageReading {
			t.Error("Visioning=false 时图片读取应为禁用")
		}
	})

	t.Run("Visioning=true 时启用图片读取", func(t *testing.T) {
		rt := newTestRuntime(t, WithModel(config.ModelConfig{Name: "m", Provider: "mock", Visioning: true}))
		tool, ok := rt.ToolRegistry().Get("Read")
		if !ok {
			t.Fatal("Read 工具未注册")
		}
		read, ok := tool.(*tools.Read)
		if !ok {
			t.Fatalf("Read 工具类型断言失败: %T", tool)
		}
		if !read.Limits().EnableImageReading {
			t.Error("Visioning=true 时图片读取应启用")
		}
	})
}
