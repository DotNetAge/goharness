package agents

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/DotNetAge/gochat"
	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/events"
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

	// OnRetry 是建流重试的可选通知回调（可预知的错误必须冒泡而非静默重试）。
	// 每次退避等待前回调一次（Phase=retry），重试后成功建流再回调一次
	// （Phase=recovered）。未设置时重试行为保持静默。
	OnRetry func(info LLMRetryNotice)
}

// LLM 重试通知的阶段取值，与 events.LLMRetryPhase* 对应。
const (
	// LLMRetryPhaseRetry 表示即将退避等待后重试。
	LLMRetryPhaseRetry = events.LLMRetryPhaseRetry

	// LLMRetryPhaseRecovered 表示重试后已成功建立流。
	LLMRetryPhaseRecovered = events.LLMRetryPhaseRecovered
)

// LLMRetryNotice 承载一次建流重试通知的详细信息。
type LLMRetryNotice struct {
	// Provider 是当前模型的服务商名。
	Provider string

	// Model 是当前模型名。
	Model string

	// StatusCode 是导致重试的 HTTP 状态码（网络错误为 0）。
	StatusCode int

	// Attempt 是即将进行的重试序号（从 1 开始）；
	// recovered 阶段为实际发生的重试次数。
	Attempt int

	// MaxAttempts 是最大重试次数（不含首次请求）。
	MaxAttempts int

	// Delay 是本次重试前的退避等待时长（recovered 阶段为 0）。
	Delay time.Duration

	// Err 是触发重试的错误。
	Err error

	// Phase 为 "retry" 或 "recovered"。
	Phase string
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

// 退避基数：限流类错误（429）的限流窗口通常以秒计，短暂退避没有实际意义，
// 使用更长的基数让重试真正有机会穿过限流窗口；5xx/网络错误维持短退避。
// 声明为变量以便测试注入极短退避，避免用例真实等待数秒。
var (
	rateLimitBackoffBase = 5 * time.Second
	defaultBackoffBase   = 500 * time.Millisecond
)

// Stream 根据请求向 LLM 发起流式调用，并对建流阶段的可重试错误做指数退避重试。
// 流式特例：只对「建流请求失败（GetStream 返回 error）」重试；流一旦建立，其内部的错误由
// 上层通过事件消费，不再重连。
// 每次退避等待前与重试成功建流后，通过 req.OnRetry 回调通知调用方（未设置则静默）。
func (c *gochatLLMClient) Stream(ctx context.Context, req LLMRequest) (*gochatcore.Stream, error) {
	notify := func(n LLMRetryNotice) {
		if req.OnRetry == nil {
			return
		}
		n.Provider = c.provider
		n.Model = req.Model
		req.OnRetry(n)
	}
	return retryStream(ctx, func() (*gochatcore.Stream, error) {
		return c.buildStream(ctx, req)
	}, notify)
}

// retryStream 对 build 返回的可重试错误做指数退避重试（最多 maxStreamRetryAttempts 次额外尝试）。
// 非可重试错误（请求方 4xx 客户端错误）与 ctx 取消直接透传。
// notify 在每次退避等待前（Phase=retry，携带实际等待时长）与重试成功建流后
// （Phase=recovered）被调用；可为 nil。
func retryStream(ctx context.Context, build func() (*gochatcore.Stream, error), notify func(LLMRetryNotice)) (*gochatcore.Stream, error) {
	var lastErr error
	retries := 0
	for attempt := 0; attempt <= maxStreamRetryAttempts; attempt++ {
		if attempt > 0 {
			// 退避基数按错误类型选择：限流（429）用长基数，其余维持短基数。
			base := defaultBackoffBase
			if isRateLimitError(lastErr) {
				base = rateLimitBackoffBase
			}
			delay := gochatcore.ExponentialBackoff(attempt-1, base)
			if notify != nil {
				notify(LLMRetryNotice{
					StatusCode:  statusCodeOf(lastErr),
					Attempt:     attempt,
					MaxAttempts: maxStreamRetryAttempts,
					Delay:       delay,
					Err:         lastErr,
					Phase:       LLMRetryPhaseRetry,
				})
			}
			if err := waitRetry(ctx, delay); err != nil {
				return nil, err
			}
		}
		st, err := build()
		if err == nil {
			// 经历过重试后成功建流：通知 recovered，让前端消除重试警告。
			if retries > 0 && notify != nil {
				notify(LLMRetryNotice{
					Attempt:     retries,
					MaxAttempts: maxStreamRetryAttempts,
					Phase:       LLMRetryPhaseRecovered,
				})
			}
			return st, nil
		}
		// 非可重试错误或上下文已取消（用户停止），不再重试
		if !gochatcore.IsRetryableError(err) || ctx.Err() != nil {
			return nil, err
		}
		lastErr = err
		retries++
	}
	return nil, lastErr
}

// waitRetry 等待指定时长；等待期间 ctx 取消则直接返回。
func waitRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// isRateLimitError 判断错误是否为限流类（gochat 的 ErrorTypeRateLimit 或 HTTP 429）。
func isRateLimitError(err error) bool {
	var apiErr *gochatcore.Error
	return errors.As(err, &apiErr) &&
		(apiErr.Type == gochatcore.ErrorTypeRateLimit || apiErr.StatusCode == http.StatusTooManyRequests)
}

// statusCodeOf 从错误中提取 HTTP 状态码；非 gochat API 错误返回 0。
func statusCodeOf(err error) int {
	var apiErr *gochatcore.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode
	}
	return 0
}

// IsPaymentRequiredLLMError 判断错误是否为 LLM 服务端返回的 402（账户欠费/余额不足）。
// 欠费不属于瞬时故障，重试无意义——调用方应冒泡错误提示，引导用户充值或更换服务商。
func IsPaymentRequiredLLMError(err error) bool {
	var apiErr *gochatcore.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusPaymentRequired
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

// chatVersionRe 与 gochat openai 客户端的版本段判断一致：
// baseURL 末尾已带 /vN 段则不再补 /v1（避免给 /v3、/v4 等地址错误追加 /v1）。
var chatVersionRe = regexp.MustCompile(`/v\d+$`)

// IsUnauthorizedLLMError 判断错误是否为 LLM 服务端返回的 401 认证失败（如 API Key not exists）。
// gochat 的 API 错误通过 core.Error.StatusCode 携带 HTTP 状态码。
func IsUnauthorizedLLMError(err error) bool {
	var apiErr *gochatcore.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized
}

// llmAuthDebugInfo 从 LLMClient 接口提取 401 排障上下文；非 gochat 实现（如测试 mock）返回空串。
func llmAuthDebugInfo(client LLMClient) string {
	gc, ok := client.(*gochatLLMClient)
	if !ok {
		return ""
	}
	return gc.authDebugContext()
}

// authDebugContext 返回 401 排障所需的实际请求上下文：request.url 与 Authorization header。
// 拼接与解析规则复刻 gochat 客户端实现，保证与实际发送的请求一致：
//   - openai 兼容：baseURL 末尾无 /vN 段则补 /v1，再拼 /chat/completions；
//     Authorization 为 Bearer <GetAPIKey(apiKey)>——注意 GetAPIKey 会先把 apiKey 大写化
//     （`-` 替换为 `_`）当作环境变量名读取，读取成功则发送环境变量值而非配置原文，
//     这是「配置了 key 却仍报 API Key not exists」的常见根因
//   - ollama：baseURL 去 /v1 后缀与结尾斜杠后拼 /api/chat，不带 Authorization header
//
// 返回值包含 API Key 明文，仅用于本地日志排障，不得输出到用户可见的事件流。
func (c *gochatLLMClient) authDebugContext() string {
	if c.isOllama() {
		baseURL := strings.TrimSuffix(c.baseURL, "/v1")
		baseURL = strings.TrimSuffix(baseURL, "/")
		return fmt.Sprintf("request_url=%s/api/chat, authorization=（ollama 原生接口不带该 header）", baseURL)
	}
	u := strings.TrimSuffix(c.baseURL, "/")
	if !chatVersionRe.MatchString(u) {
		u += "/v1"
	}
	u += "/chat/completions"
	var auth string
	if c.apiKey != "" {
		auth = "Bearer " + gochatcore.GetAPIKey(c.apiKey)
	}
	return fmt.Sprintf("request_url=%s, authorization=%q", u, auth)
}
