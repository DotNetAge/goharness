package agents

import (
	"context"
	"strings"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/events"
	"github.com/DotNetAge/goharness/logging"
)

// clientAdapter 将 goharness 的 LLMClient（Stream 接口 + 建流重试/超时/取消事件）
// 适配为 goagent 引擎所需的 gochat core.Client。
//
// 保留的 goharness 独有健壮性（计划 §必留能力表）：
//   - 建流重试：429 限流 / 5xx / 网络错误的指数退避（llm_client.go retryStream），
//     重试通知经构造时注入的 emit 回调直接发射 LLMRetry 事件（客户端级事件，
//     不经 goagent 引擎，与事件分类处置表一致）；
//   - 单次调用超时预算：模型 RequestTimeout > ctx 剩余 > 默认 4 分钟；
//   - 401/402 排障：认证失败调试信息仅写本地日志，不进入事件流。
//
// 适配器按 Run 粒度创建（emit 闭包绑定单次 Ask 的事件装配），不可跨 Run 复用。
type clientAdapter struct {
	client LLMClient
	model  config.ModelConfig
	logger logging.Logger
	// onRetry 将建流重试通知发射为 goharness LLMRetry 事件（可空）。
	onRetry func(events.ReactEventType, any)
}

// newClientAdapter 包装 goharness LLMClient 为 gochat core.Client。
func newClientAdapter(client LLMClient, model config.ModelConfig, logger logging.Logger, emit func(events.ReactEventType, any)) *clientAdapter {
	return &clientAdapter{client: client, model: model, logger: logger, onRetry: emit}
}

// ChatStream 实现核心流式调用：合并 goagent 传入的请求选项（工具清单），
// 组装 LLMRequest 后委托 goharness LLMClient.Stream（内含重试与超时）。
func (c *clientAdapter) ChatStream(ctx context.Context, messages []gochatcore.Message, opts ...gochatcore.Option) (*gochatcore.Stream, error) {
	// 解析 goagent 传来的请求级选项：工具清单是关键载荷（模型据此发起工具调用）
	var reqOpts gochatcore.Options
	for _, opt := range opts {
		opt(&reqOpts)
	}

	llmTimeout := llmCallTimeout(c.model.RequestTimeout, ctx)
	stream, err := c.client.Stream(ctx, LLMRequest{
		Messages:          messages,
		Model:             c.model.Name,
		Temperature:       c.model.Temperature,
		TopP:              c.model.TopP,
		TopK:              c.model.TopK,
		RepetitionPenalty: c.model.RepetitionPenalty,
		FrequencyPenalty:  c.model.FrequencyPenalty,
		Tools:             reqOpts.Tools,
		ToolChoice:        "auto",
		Timeout:           llmTimeout,
		// 建流重试必须冒泡（与旧核口径一致）：限流/5xx 等可预知错误若静默重试，
		// 前端会一直显示「正在处理」，用户无从得知还要等多久。
		OnRetry: func(n LLMRetryNotice) {
			if c.onRetry == nil {
				return
			}
			c.onRetry(events.LLMRetry, events.LLMRetryData{
				SessionID:   "",
				Provider:    n.Provider,
				Model:       n.Model,
				StatusCode:  n.StatusCode,
				Attempt:     n.Attempt,
				MaxAttempts: n.MaxAttempts,
				RetryAfter:  n.Delay,
				Error:       errString(n.Err),
				Phase:       phaseOf(n.Phase),
			})
		},
	})
	if err != nil {
		// 401 认证失败时输出实际请求的 url 与 Authorization（仅本地日志，不进事件流）
		if IsUnauthorizedLLMError(err) && c.logger != nil {
			c.logger.Error("LLM认证失败(401)调试信息", err, "debug", llmAuthDebugInfo(c.client))
		}
		// 402 欠费：可预知的终止性错误，冒泡人话提示
		if IsPaymentRequiredLLMError(err) && c.logger != nil {
			c.logger.Error("LLM服务商返回402（账户欠费）", err, "provider", c.model.Provider)
		}
		return nil, err
	}
	return stream, nil
}

// Chat 实现非流式调用：goagent 换核装配恒走流式（ChatStreamCtx，保证增量事件），
// 此实现仅为满足 core.Client 接口完整性——内部复用流式调用收集完整响应。
func (c *clientAdapter) Chat(ctx context.Context, messages []gochatcore.Message, opts ...gochatcore.Option) (*gochatcore.Response, error) {
	stream, err := c.ChatStream(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	var content, reasoning strings.Builder
	for stream.Next() {
		ev := stream.Event()
		if ev.Err != nil {
			return nil, ev.Err
		}
		switch ev.Type {
		case gochatcore.EventContent:
			content.WriteString(ev.Content)
		case gochatcore.EventThinking:
			reasoning.WriteString(ev.Content)
		}
	}
	finishReason := ""
	if ev := stream.Event(); ev.Type == gochatcore.EventDone {
		finishReason = ev.FinishReason
	}
	return &gochatcore.Response{
		Content:          content.String(),
		ReasoningContent: reasoning.String(),
		Message: gochatcore.Message{
			Role:             gochatcore.RoleAssistant,
			Content:          []gochatcore.ContentBlock{{Type: gochatcore.ContentTypeText, Text: content.String()}},
			ReasoningContent: reasoning.String(),
			ToolCalls:        stream.ToolCalls(),
		},
		FinishReason: finishReason,
		Usage:        stream.Usage(),
	}, nil
}

// phaseOf 将 LLMRetryNotice 的阶段值转换为事件数据所需的阶段字符串。
func phaseOf(phase string) string {
	if phase == LLMRetryPhaseRecovered {
		return events.LLMRetryPhaseRecovered
	}
	return events.LLMRetryPhaseRetry
}

// errString 安全提取错误文本（可空）。
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
