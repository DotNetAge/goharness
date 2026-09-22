package agents

import (
	"context"
	"errors"
	"testing"
	"time"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/events"
)

// retryFakeClient 模拟建流重试：Stream 时依次回放注入的重试通知后成功建流。
type retryFakeClient struct {
	notices []LLMRetryNotice
}

func (f *retryFakeClient) Stream(ctx context.Context, req LLMRequest) (*gochatcore.Stream, error) {
	for _, n := range f.notices {
		if req.OnRetry != nil {
			req.OnRetry(n)
		}
	}
	return &gochatcore.Stream{}, nil
}

// TestClientAdapter_OnRetryEmitsLLMRetry 验证建流重试通知经 clientAdapter 桥接为
// LLMRetry 事件且字段映射正确——这是换核后 LLMRetry 事件链
// （LLMClient 建流重试 → adapter → emit → AskBuilder.OnLLMRetry → daemon → 前端）
// 在 goharness 侧的最后一环，必须端到端有显式证据。
func TestClientAdapter_OnRetryEmitsLLMRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	fake := &retryFakeClient{notices: []LLMRetryNotice{
		{Provider: "deepseek", Model: "deepseek-chat", StatusCode: 429, Attempt: 1,
			MaxAttempts: 3, Delay: 500 * time.Millisecond, Err: errors.New("slow down"), Phase: LLMRetryPhaseRetry},
		{Provider: "deepseek", Model: "deepseek-chat", Phase: LLMRetryPhaseRecovered},
	}}

	var emitted []events.LLMRetryData
	adapter := newClientAdapter(fake,
		config.ModelConfig{Name: "deepseek-chat", Provider: "deepseek"}, nil,
		func(typ events.ReactEventType, data any) {
			if typ != events.LLMRetry {
				t.Errorf("应只发射 LLMRetry 事件，实际 %q", typ)
				return
			}
			emitted = append(emitted, data.(events.LLMRetryData))
		})

	if _, err := adapter.ChatStream(ctx, nil); err != nil {
		t.Fatalf("重试后应成功建流，实际错误: %v", err)
	}

	if len(emitted) != 2 {
		t.Fatalf("应发射 2 次 LLMRetry 事件（retry + recovered），实际 %d 次", len(emitted))
	}
	first := emitted[0]
	if first.Provider != "deepseek" || first.Model != "deepseek-chat" {
		t.Errorf("retry 事件应携带服务商与模型，实际 provider=%q model=%q", first.Provider, first.Model)
	}
	if first.StatusCode != 429 || first.Attempt != 1 || first.MaxAttempts != 3 {
		t.Errorf("retry 事件字段映射错误: statusCode=%d attempt=%d maxAttempts=%d",
			first.StatusCode, first.Attempt, first.MaxAttempts)
	}
	if first.RetryAfter != 500*time.Millisecond {
		t.Errorf("retry 事件应携带退避时长 500ms，实际 %v", first.RetryAfter)
	}
	if first.Error != "slow down" {
		t.Errorf("retry 事件应携带错误文本，实际 %q", first.Error)
	}
	if first.Phase != events.LLMRetryPhaseRetry {
		t.Errorf("retry 事件阶段应为 retry，实际 %q", first.Phase)
	}
	if emitted[1].Phase != events.LLMRetryPhaseRecovered {
		t.Errorf("recovered 事件阶段应为 recovered，实际 %q", emitted[1].Phase)
	}
}
