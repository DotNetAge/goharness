package action

import (
	"fmt"

	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
)

// ImageHook 负责将工具返回的图片数据（如 Read 读取的图片文件）转换为
// 图片内容块，使图片以 image_url 消息的形式进入上下文。
//
// 与 Read 的关系：Read 工具只负责"读取"图片并返回原始数据（ReadResult.Images），
// 本 Hook 负责"注入"——在 ToolHook.After 阶段把图片从工具结果中提取出来，
// 转换为 session.ImageBlock 存入 ToolResult.ImageBlocks，由 executor 持久化时
// 追加为独立的图片消息（user 角色），而非混入工具结果的文本内容。
//
// 仅在模型支持视觉理解（Visioning=true）时注册；非视觉模型不会注册该 Hook，
// 图片数据自然不会被转换为视觉消息，不会浪费上下文。
type ImageHook struct {
	Logger logging.Logger
}

// NewImageHook 创建图片提取 Hook。logger 可为 nil（静默模式）。
func NewImageHook(logger logging.Logger) *ImageHook {
	return &ImageHook{Logger: logger}
}

// Priority 返回钩子优先级：在默认工具钩子（Permission 41 / FileModify 42 /
// ToolLogger 46）之后执行，避免与它们的 Abort 逻辑交错。
func (h *ImageHook) Priority() int { return hooks.PriorityToolLogger + 1 }

// Before 不干预工具执行前的流程。
func (h *ImageHook) Before(sessionID string, toolName string, params map[string]any) hooks.HookResult {
	return hooks.HookResult{}
}

// After 将 ToolResult.Images 转换为图片内容块。
// 转换结果写入 ToolResult.ImageBlocks，供 executor 生成图片消息。
func (h *ImageHook) After(result *hooks.ToolResult) hooks.HookResult {
	if result == nil || len(result.Images) == 0 {
		return hooks.HookResult{}
	}
	for _, img := range result.Images {
		if img.Base64Data == "" {
			continue
		}
		alt := fmt.Sprintf("%dx%d", img.Width, img.Height)
		if img.Width == 0 || img.Height == 0 {
			alt = img.MediaType
		}
		result.ImageBlocks = append(result.ImageBlocks, session.ImageBlock{
			MediaType:  img.MediaType,
			Base64Data: img.Base64Data,
			AltText:    alt,
		})
	}
	if h.Logger != nil && len(result.ImageBlocks) > 0 {
		h.Logger.Debug("图片已提取为视觉消息",
			"tool", result.ToolName,
			"tool_call_id", result.ToolCallID,
			"images", len(result.ImageBlocks),
		)
	}
	return hooks.HookResult{}
}

// Abort 是 ImageHook 的空实现。
func (h *ImageHook) Abort(reason string) {}
