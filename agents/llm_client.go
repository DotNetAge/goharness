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

func (c *gochatLLMClient) Stream(ctx context.Context, req LLMRequest) (*gochatcore.Stream, error) {
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
