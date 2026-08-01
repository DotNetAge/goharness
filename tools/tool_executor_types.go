package tools

import (
	"context"
	"time"
)

type ToolExecutionResult struct {
	Result   string
	Metadata any
	// Images 是工具返回的原始图片数据（如 Read 读取的图片文件）。
	// 图片不与文本结果混在一起序列化，而是独立携带，供上层 Hook 提取后
	// 以 image_url 消息的形式进入上下文。
	Images   []ImageContent
	Duration time.Duration
	Error    error
	ToolName string
}

type ToolExecutor interface {
	Execute(ctx context.Context, name string, params map[string]any) (*ToolExecutionResult, error)
	ResetCycle()
}
