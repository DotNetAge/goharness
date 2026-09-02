package agents

import (
	"context"
	"testing"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
)

// helper：构造基于状态码的 core.Error（429→RateLimit / 503→可重试 5xx / 400→不可重试）
func apiErr(status int, body string) error {
	return gochatcore.NewAPIErrorFromResponse(status, []byte(body))
}

// withShortBackoff 将退避基数替换为测试用极短值，避免用例真实等待数秒；
// 返回恢复函数，用 defer 调用。
func withShortBackoff(rateLimit, def time.Duration) func() {
	oldRate, oldDef := rateLimitBackoffBase, defaultBackoffBase
	rateLimitBackoffBase = rateLimit
	defaultBackoffBase = def
	return func() {
		rateLimitBackoffBase = oldRate
		defaultBackoffBase = oldDef
	}
}

// TestRetryStream_RetryableThenSucceeds 验证可重试错误（429）退避后重新建流成功。
func TestRetryStream_RetryableThenSucceeds(t *testing.T) {
	// 项目规范：测试必须包含 10 秒超时
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer withShortBackoff(time.Millisecond, time.Millisecond)()

	calls := 0
	st, err := retryStream(ctx, func() (*gochatcore.Stream, error) {
		calls++
		if calls == 1 {
			return nil, apiErr(429, `{"error":{"message":"slow down"}}`)
		}
		return &gochatcore.Stream{}, nil
	}, nil)
	if err != nil {
		t.Fatalf("重试后应成功，实际错误: %v", err)
	}
	if st == nil {
		t.Fatal("应返回非 nil 的流")
	}
	if calls != 2 {
		t.Fatalf("应调用 build 2 次（首次失败 + 一次重试），实际 %d 次", calls)
	}
}

// TestRetryStream_NoRetryOnClientError 验证请求方错误（400）不重试、直接透传。
func TestRetryStream_NoRetryOnClientError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	calls := 0
	_, err := retryStream(ctx, func() (*gochatcore.Stream, error) {
		calls++
		return nil, apiErr(400, `{"error":{"message":"bad request"}}`)
	}, nil)
	if err == nil {
		t.Fatal("应返回错误")
	}
	if calls != 1 {
		t.Fatalf("客户端错误不应重试，build 应只调用 1 次，实际 %d 次", calls)
	}
}

// TestRetryStream_ServerErrorRetried 验证 5xx 服务端错误参与重试。
func TestRetryStream_ServerErrorRetried(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer withShortBackoff(time.Millisecond, time.Millisecond)()

	calls := 0
	st, err := retryStream(ctx, func() (*gochatcore.Stream, error) {
		calls++
		if calls < 3 {
			return nil, apiErr(503, `{"error":{"message":"unavailable"}}`)
		}
		return &gochatcore.Stream{}, nil
	}, nil)
	if err != nil {
		t.Fatalf("5xx 重试后应成功，实际错误: %v", err)
	}
	if st == nil {
		t.Fatal("应返回非 nil 的流")
	}
	if calls != 3 {
		t.Fatalf("5xx 应重试（最终成功用 3 次），实际 %d 次", calls)
	}
}

// TestRetryStream_NotifiesRetryAndRecovered 验证重试过程发出 retry 通知、
// 成功建流后发出 recovered 通知（可预知错误必须冒泡，不得静默重试）。
func TestRetryStream_NotifiesRetryAndRecovered(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer withShortBackoff(time.Millisecond, time.Millisecond)()

	calls := 0
	var notices []LLMRetryNotice
	st, err := retryStream(ctx, func() (*gochatcore.Stream, error) {
		calls++
		if calls == 1 {
			return nil, apiErr(429, `{"error":{"message":"slow down"}}`)
		}
		return &gochatcore.Stream{}, nil
	}, func(n LLMRetryNotice) {
		notices = append(notices, n)
	})
	if err != nil {
		t.Fatalf("重试后应成功，实际错误: %v", err)
	}
	if st == nil {
		t.Fatal("应返回非 nil 的流")
	}
	if len(notices) != 2 {
		t.Fatalf("应发出 2 次通知（retry + recovered），实际 %d 次: %+v", len(notices), notices)
	}
	retryNotice := notices[0]
	if retryNotice.Phase != LLMRetryPhaseRetry {
		t.Errorf("第一条通知应为 retry 阶段，实际 %q", retryNotice.Phase)
	}
	if retryNotice.Attempt != 1 {
		t.Errorf("retry 通知的 Attempt 应为 1，实际 %d", retryNotice.Attempt)
	}
	if retryNotice.StatusCode != 429 {
		t.Errorf("retry 通知应携带状态码 429，实际 %d", retryNotice.StatusCode)
	}
	if retryNotice.Delay <= 0 {
		t.Errorf("retry 通知应携带正的退避时长，实际 %v", retryNotice.Delay)
	}
	recovered := notices[1]
	if recovered.Phase != LLMRetryPhaseRecovered {
		t.Errorf("第二条通知应为 recovered 阶段，实际 %q", recovered.Phase)
	}
	if recovered.Delay != 0 {
		t.Errorf("recovered 通知不应携带退避时长，实际 %v", recovered.Delay)
	}
}

// TestRetryStream_RateLimitUsesLongBackoff 验证限流错误（429）使用长退避基数，
// 5xx 等其它可重试错误维持短退避基数。
func TestRetryStream_RateLimitUsesLongBackoff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// 限流基数 100ms，默认基数 1ms：两类错误的退避时长应有可区分的数量级差异
	defer withShortBackoff(100*time.Millisecond, time.Millisecond)()

	var rateLimitDelay, serverErrDelay time.Duration
	retryStream(ctx, func() (*gochatcore.Stream, error) {
		return nil, apiErr(429, `{"error":{"message":"slow down"}}`)
	}, func(n LLMRetryNotice) {
		if n.Delay > rateLimitDelay {
			rateLimitDelay = n.Delay
		}
	})
	retryStream(ctx, func() (*gochatcore.Stream, error) {
		return nil, apiErr(503, `{"error":{"message":"unavailable"}}`)
	}, func(n LLMRetryNotice) {
		if n.Delay > serverErrDelay {
			serverErrDelay = n.Delay
		}
	})
	if rateLimitDelay < 100*time.Millisecond {
		t.Errorf("限流错误应使用长退避基数（>=100ms），实际最大 %v", rateLimitDelay)
	}
	if serverErrDelay >= 100*time.Millisecond {
		t.Errorf("5xx 错误应使用短退避基数（<100ms），实际最大 %v", serverErrDelay)
	}
}

// TestRetryStream_ExhaustionNotifiesEachAttempt 验证重试耗尽时每次退避都发出通知、
// 且不发出 recovered 通知（最终失败由上层 Error 事件收尾）。
func TestRetryStream_ExhaustionNotifiesEachAttempt(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer withShortBackoff(time.Millisecond, time.Millisecond)()

	retryNotices := 0
	recovered := false
	_, err := retryStream(ctx, func() (*gochatcore.Stream, error) {
		return nil, apiErr(503, `{"error":{"message":"unavailable"}}`)
	}, func(n LLMRetryNotice) {
		if n.Phase == LLMRetryPhaseRecovered {
			recovered = true
			return
		}
		retryNotices++
	})
	if err == nil {
		t.Fatal("重试耗尽后应返回错误")
	}
	if retryNotices != maxStreamRetryAttempts {
		t.Errorf("应发出 %d 次 retry 通知，实际 %d 次", maxStreamRetryAttempts, retryNotices)
	}
	if recovered {
		t.Error("重试耗尽失败时不应发出 recovered 通知")
	}
}

// TestIsPaymentRequiredLLMError 验证 402 欠费错误识别。
func TestIsPaymentRequiredLLMError(t *testing.T) {
	if !IsPaymentRequiredLLMError(apiErr(402, `{"error":{"message":"insufficient balance"}}`)) {
		t.Error("402 应被识别为欠费错误")
	}
	if IsPaymentRequiredLLMError(apiErr(429, `{"error":{"message":"slow down"}}`)) {
		t.Error("429 不应被识别为欠费错误")
	}
	if IsPaymentRequiredLLMError(nil) {
		t.Error("nil 不应被识别为欠费错误")
	}
}

// TestPaymentRequiredNotRetryable 验证 402 欠费不属于可重试错误：
// 欠费是终止性错误，重试无意义，必须直接透传给上层冒泡提示。
func TestPaymentRequiredNotRetryable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	calls := 0
	_, err := retryStream(ctx, func() (*gochatcore.Stream, error) {
		calls++
		return nil, apiErr(402, `{"error":{"message":"insufficient balance"}}`)
	}, nil)
	if err == nil {
		t.Fatal("402 应直接透传错误")
	}
	if calls != 1 {
		t.Fatalf("402 不应重试，build 应只调用 1 次，实际 %d 次", calls)
	}
}
