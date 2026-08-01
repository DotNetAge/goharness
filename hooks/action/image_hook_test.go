package action

import (
	"testing"

	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/tools"
)

// TestImageHook_After 验证 After 阶段将工具返回的图片数据转换为图片内容块。
func TestImageHook_After(t *testing.T) {
	t.Run("无图片时不转换", func(t *testing.T) {
		h := NewImageHook(nil)
		tr := &hooks.ToolResult{ToolName: "Read", Result: "普通文本结果"}
		hr := h.After(tr)
		if hr.IsTerminal() {
			t.Fatalf("无图片时不应终止: %v", hr)
		}
		if len(tr.ImageBlocks) != 0 {
			t.Errorf("无图片时 ImageBlocks 应为空，得到 %d", len(tr.ImageBlocks))
		}
	})

	t.Run("有图片时转换为图片块", func(t *testing.T) {
		h := NewImageHook(nil)
		tr := &hooks.ToolResult{
			ToolName: "Read",
			Images: []tools.ImageContent{
				{MediaType: "image/png", Base64Data: "aGVsbG8=", Width: 512, Height: 300},
				{MediaType: "image/jpeg", Base64Data: "", Width: 100, Height: 100}, // 空数据应被跳过
			},
		}
		h.After(tr)
		if len(tr.ImageBlocks) != 1 {
			t.Fatalf("期望 1 个图片块，得到 %d", len(tr.ImageBlocks))
		}
		blk := tr.ImageBlocks[0]
		if blk.MediaType != "image/png" {
			t.Errorf("MediaType 不匹配: %q", blk.MediaType)
		}
		if blk.Base64Data != "aGVsbG8=" {
			t.Errorf("Base64Data 不匹配: %q", blk.Base64Data)
		}
		if blk.AltText != "512x300" {
			t.Errorf("AltText 应包含尺寸信息: %q", blk.AltText)
		}
	})

	t.Run("无尺寸信息时 AltText 回退为 MIME 类型", func(t *testing.T) {
		h := NewImageHook(nil)
		tr := &hooks.ToolResult{
			ToolName: "Read",
			Images: []tools.ImageContent{
				{MediaType: "image/svg+xml", Base64Data: "c3Zn"},
			},
		}
		h.After(tr)
		if len(tr.ImageBlocks) != 1 {
			t.Fatalf("期望 1 个图片块，得到 %d", len(tr.ImageBlocks))
		}
		if tr.ImageBlocks[0].AltText != "image/svg+xml" {
			t.Errorf("AltText 应回退为 MIME 类型: %q", tr.ImageBlocks[0].AltText)
		}
	})
}
