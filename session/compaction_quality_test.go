package session

import (
	"context"
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/logging"
)

func TestEvaluateCompactionQuality_CountsKeptAnchors(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "读 /Users/ray/workspaces/a/main.go，结论写进 E1-04"},
		{Role: "assistant", Content: "参考 `dedupCacheKey` 的实现"},
	}
	summary := "阅读了 /Users/ray/workspaces/a/main.go，依据 E1-04 得出结论。"

	q := EvaluateCompactionQuality(msgs, summary)

	// 锚点：/Users/ray/workspaces/a/main.go、E1-04、dedupCacheKey
	if q.TotalAnchors != 3 {
		t.Fatalf("期望 3 个锚点，实际 %d（缺失样例 %v）", q.TotalAnchors, q.MissingSamples)
	}
	if q.KeptAnchors != 2 {
		t.Errorf("期望保留 2 个（路径 + 编号），实际 %d", q.KeptAnchors)
	}
	// dedupCacheKey 未出现在摘要中
	if len(q.MissingSamples) != 1 || q.MissingSamples[0] != "dedupCacheKey" {
		t.Errorf("缺失样例应为 dedupCacheKey，实际 %v", q.MissingSamples)
	}
	if q.Coverage < 0.66 || q.Coverage > 0.67 {
		t.Errorf("覆盖率应约为 0.667，实际 %f", q.Coverage)
	}
}

// 摘要把关键路径全部概括掉时，覆盖率必须明显下降——这是「静默失忆」的信号。
func TestEvaluateCompactionQuality_DetectsHeavyLoss(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "处理 /Users/ray/a/x.go 与 /Users/ray/b/y.go，对应 C-03 与 C-07"},
	}
	summary := "处理了两个文件的相关条目。"

	q := EvaluateCompactionQuality(msgs, summary)
	if q.TotalAnchors != 4 {
		t.Fatalf("期望 4 个锚点，实际 %d", q.TotalAnchors)
	}
	if q.Coverage > 0.01 {
		t.Errorf("关键锚点全丢，覆盖率应接近 0，实际 %f", q.Coverage)
	}
	if q.Coverage >= CompactionMinCoverage {
		t.Error("覆盖率应低于告警下限")
	}
}

// 纯叙述性窗口没有锚点，判为无损失，避免误报。
func TestEvaluateCompactionQuality_NoAnchors(t *testing.T) {
	msgs := []Message{{Role: "user", Content: "你好，帮我看看这个问题"}}
	q := EvaluateCompactionQuality(msgs, "用户问了个问题")
	if q.TotalAnchors != 0 {
		t.Errorf("不应识别出锚点，实际 %d", q.TotalAnchors)
	}
	if q.Coverage != 1 {
		t.Errorf("无锚点时覆盖率应记为 1，实际 %f", q.Coverage)
	}
}

type stubCompactor struct {
	summary string
	calls   int
}

func (s *stubCompactor) Compact(_ context.Context, _ *Session, _ []Message) (string, error) {
	s.calls++
	return s.summary, nil
}

// ForceExtractMemory 只沉淀记忆，不移动 cursor —— 上下文窗口必须原封不动。
func TestForceExtractMemory_KeepsCursorAndDeliversSummary(t *testing.T) {
	ctx := context.Background()
	s, err := New("agent", "", t.TempDir(), newMockStore(), logging.NewNopLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Append(ctx,
		Message{Role: "user", Content: "请读 /Users/ray/a/b.go"},
		Message{Role: "assistant", Content: "已读取"},
	); err != nil {
		t.Fatalf("Append: %v", err)
	}

	comp := &stubCompactor{summary: "读了 /Users/ray/a/b.go"}
	var delivered string
	s.SetCompactor(comp)
	s.SetSummarySink(func(_ context.Context, summary string) error {
		delivered = summary
		return nil
	})

	before := s.Cursor()
	s.ForceExtractMemory(ctx)
	after := s.Cursor()

	if comp.calls != 1 {
		t.Errorf("应调用压缩器一次，实际 %d 次", comp.calls)
	}
	if delivered == "" {
		t.Error("摘要应交付给 SummarySink")
	}
	if before != after {
		t.Errorf("cursor 不应移动：之前 %d，之后 %d", before, after)
	}
	if got := len(s.All()); got != 2 {
		t.Errorf("消息不应被删除，期望 2 条，实际 %d 条", got)
	}
}

// 窗口为空时直接跳过，不调用压缩器、不产生成本。
func TestForceExtractMemory_SkipsEmptyWindow(t *testing.T) {
	ctx := context.Background()
	s, err := New("agent", "", t.TempDir(), newMockStore(), logging.NewNopLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	comp := &stubCompactor{summary: "不应被调用"}
	var called bool
	s.SetCompactor(comp)
	s.SetSummarySink(func(_ context.Context, summary string) error {
		called = true
		return nil
	})

	s.ForceExtractMemory(ctx)

	if comp.calls != 0 {
		t.Errorf("空窗口不应调用压缩器，实际 %d 次", comp.calls)
	}
	if called {
		t.Error("空窗口不应交付摘要")
	}
}

// 对照：ForceCompact 仍然移动 cursor（确认这次重构没有改变原语义）。
func TestForceCompact_StillSlidesCursor(t *testing.T) {
	ctx := context.Background()
	s, err := New("agent", "", t.TempDir(), newMockStore(), logging.NewNopLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// ForceCompact 有 100K 下限，直接构造一个足够大的窗口
	big := strings.Repeat("x", 400000)
	if err := s.Append(ctx, Message{Role: "user", Content: big}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	comp := &stubCompactor{summary: "摘要"}
	s.SetCompactor(comp)
	s.SetSummarySink(func(_ context.Context, _ string) error { return nil })

	s.ForceCompact(ctx)

	if comp.calls != 1 {
		t.Fatalf("应触发压缩，实际调用 %d 次", comp.calls)
	}
	if s.Cursor() == 0 {
		t.Error("ForceCompact 应移动 cursor，但仍是 0")
	}
}
