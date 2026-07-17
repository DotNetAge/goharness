package agents

import (
	"context"
	"time"

	"github.com/DotNetAge/gochat"
	gochatcore "github.com/DotNetAge/gochat/core"
)

// LLMRequest 封装单次大语言模型流式调用所需的全部参数。
type LLMRequest struct {
	Messages          []gochatcore.Message
	Model             string
	Temperature       float64
	MaxTokens         int64
	TopP              float64
	TopK              float64
	RepetitionPenalty float64
	FrequencyPenalty  float64
	Tools             []gochatcore.Tool
	ToolChoice        string
	Timeout           time.Duration
}

// LLMClient 定义与大语言模型交互的最小接口。
// 通过实现该接口，可注入 mock 客户端或支持其他大语言模型提供商。
type LLMClient interface {
	// Stream 根据请求向大语言模型发起流式调用，返回可读事件流。
	// 调用方负责关闭返回的流。
	Stream(ctx context.Context, req LLMRequest) (*gochatcore.Stream, error)
}

// gochatLLMClient 是基于 github.com/DotNetAge/gochat 的默认 LLMClient 实现。
type gochatLLMClient struct {
	apiKey  string
	baseURL string
}

// NewDefaultLLMClient 创建基于 gochat 的默认 LLMClient。
// apiKey 与 baseURL 来自模型配置，后续每次调用都会注入到 gochat 客户端。
func NewDefaultLLMClient(apiKey, baseURL string) LLMClient {
	return &gochatLLMClient{
		apiKey:  apiKey,
		baseURL: baseURL,
	}
}

func (c *gochatLLMClient) Stream(ctx context.Context, req LLMRequest) (*gochatcore.Stream, error) {
	client := gochat.Client().Config(
		gochat.WithAPIKey(c.apiKey),
		gochat.WithBaseURL(c.baseURL),
		gochat.WithTimeout(req.Timeout),
	).WithContext(ctx)

	builder := client.
		Messages(req.Messages...).
		Model(req.Model).
		Temperature(req.Temperature).
		MaxTokens(int(req.MaxTokens)).
		EnableThinking(true).
		ParallelToolCalls(true).
		ToolChoice(req.ToolChoice)

	if req.TopP > 0 {
		builder = builder.TopP(req.TopP)
	}
	if req.TopK > 0 {
		builder = builder.TopK(int(req.TopK))
	}
	if req.RepetitionPenalty != 0 {
		// RepetitionPenalty 映射为 gochat 的 PresencePenalty。
		builder = builder.PresencePenalty(req.RepetitionPenalty)
	}
	if req.FrequencyPenalty != 0 {
		builder = builder.FrequencyPenalty(req.FrequencyPenalty)
	}
	if len(req.Tools) > 0 {
		builder = builder.Tools(req.Tools...)
	}

	return builder.GetStream()
}
