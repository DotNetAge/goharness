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

// TestRetryStream_RetryableThenSucceeds 验证可重试错误（429）退避后重新建流成功。
func TestRetryStream_RetryableThenSucceeds(t *testing.T) {
	// 项目规范：测试必须包含 10 秒超时
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	calls := 0
	st, err := retryStream(ctx, func() (*gochatcore.Stream, error) {
		calls++
		if calls == 1 {
			return nil, apiErr(429, `{"error":{"message":"slow down"}}`)
		}
		return &gochatcore.Stream{}, nil
	})
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
	})
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

	calls := 0
	st, err := retryStream(ctx, func() (*gochatcore.Stream, error) {
		calls++
		if calls < 3 {
			return nil, apiErr(503, `{"error":{"message":"unavailable"}}`)
		}
		return &gochatcore.Stream{}, nil
	})
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