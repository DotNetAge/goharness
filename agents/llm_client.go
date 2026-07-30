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

	// MaxTokens == 0 表示未设置上限，让 ollama 用默认 num_predict=-1（生成到 EOS）。
	// 不能无条件调用 MaxTokens(0)，否则 gochat 会发 num_predict:0 给 ollama，
	// 导致模型立即停止生成（生成 0 个 token）。
	// 云端 OpenAI 兼容 API 同样：max_tokens 留空表示不限，传 0 会被某些 API 拒绝。
	if req.MaxTokens > 0 {
		builder = builder.MaxTokens(int(req.MaxTokens))
	}

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
