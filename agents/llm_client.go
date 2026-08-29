package agents

import (
	"context"
	"strings"
	"time"

	"github.com/DotNetAge/gochat"
	gochatcore "github.com/DotNetAge/gochat/core"
)

// LLMRequest 封装单次大语言模型流式调用所需的全部参数。
type LLMRequest struct {
	Messages          []gochatcore.Message
	Model             string
	Temperature       float64
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
	apiKey   string
	baseURL  string
	provider string
}

// NewDefaultLLMClient 创建基于 gochat 的默认 LLMClient。
// apiKey、baseURL 与 provider 来自模型配置，后续每次调用都会注入到 gochat 客户端。
// provider 决定使用哪个 gochat 客户端实现：
//   - ollama：走原生 /api/chat，可正确解析 message.thinking 字段
//   - 其余：走 OpenAI 兼容的 /v1/chat/completions
func NewDefaultLLMClient(apiKey, baseURL, provider string) LLMClient {
	return &gochatLLMClient{
		apiKey:   apiKey,
		baseURL:  baseURL,
		provider: provider,
	}
}

// isOllama 判断当前 provider 是否为 ollama。
// ollama 的 OpenAI 兼容接口把思考内容放在非标准的 reasoning 字段，
// gochat 的 OpenAI 客户端只认 reasoning_content，会导致 thinking 模型的内容全部丢失。
// 因此 ollama 必须走原生 OllamaClient，由其解析 message.thinking。
func (c *gochatLLMClient) isOllama() bool {
	return strings.EqualFold(c.provider, "ollama")
}

// maxStreamRetryAttempts 表示建流失败时的额外指数退避重试次数（不含首次请求）。
// 仅对可重试错误（429 限流 / 408 / 5xx / 网络 / 超时）重试；请求方错误（4xx 客户端错误）直接透传。
const maxStreamRetryAttempts = 3

// Stream 根据请求向 LLM 发起流式调用，并对建流阶段的可重试错误做指数退避重试。
// 流式特例：只对「建流请求失败（GetStream 返回 error）」重试；流一旦建立，其内部的错误由
// 上层通过事件消费，不再重连。
func (c *gochatLLMClient) Stream(ctx context.Context, req LLMRequest) (*gochatcore.Stream, error) {
	return retryStream(ctx, func() (*gochatcore.Stream, error) {
		return c.buildStream(ctx, req)
	})
}

// retryStream 对 build 返回的可重试错误做指数退避重试（最多 maxStreamRetryAttempts 次额外尝试）。
// 非可重试错误（请求方 4xx 客户端错误）与 ctx 取消直接透传。
func retryStream(ctx context.Context, build func() (*gochatcore.Stream, error)) (*gochatcore.Stream, error) {
	var lastErr error
	for attempt := 0; attempt <= maxStreamRetryAttempts; attempt++ {
		if attempt > 0 {
			if err := waitRetry(ctx, attempt-1); err != nil {
				return nil, err
			}
		}
		st, err := build()
		if err == nil {
			return st, nil
		}
		// 非可重试错误或上下文已取消（用户停止），不再重试
		if !gochatcore.IsRetryableError(err) || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
	}
	return nil, lastErr
}

// waitRetry 按指数退避（含抖动）等待；等待期间 ctx 取消则直接返回。
func waitRetry(ctx context.Context, attempt int) error {
	delay := gochatcore.ExponentialBackoff(attempt, 500*time.Millisecond)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// buildStream 构造 gochat 客户端并返回事件流；失败时返回可判定重试性的 core.Error。
func (c *gochatLLMClient) buildStream(ctx context.Context, req LLMRequest) (*gochatcore.Stream, error) {
	baseURL := c.baseURL
	if c.isOllama() {
		// ollama 原生 /api/chat 接口不使用 /v1 前缀。
		// providers.yml 中 ollama 的 base_url 通常配置为 OpenAI 兼容路径（带 /v1），
		// 这里去掉 /v1 与结尾斜杠，避免拼成 .../v1/api/chat 导致 404。
		baseURL = strings.TrimSuffix(baseURL, "/v1")
		baseURL = strings.TrimSuffix(baseURL, "/")
	}
	client := gochat.Client().Config(
		gochat.WithAPIKey(c.apiKey),
		gochat.WithBaseURL(baseURL),
		gochat.WithTimeout(req.Timeout),
	).WithContext(ctx)

	builder := client.
		Messages(req.Messages...).
		Model(req.Model).
		Temperature(req.Temperature).
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

	// provider 路由：ollama 走原生 OllamaClient 解析 message.thinking，
	// 其余走默认 OpenAIClient 解析 reasoning_content。
	if c.isOllama() {
		return builder.GetStreamFor(gochat.OllamaClient)
	}
	return builder.GetStream()
}
