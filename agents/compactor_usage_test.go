package agents

import (
	"context"
	"testing"

	gochatcore "github.com/DotNetAge/gochat/core"
	"github.com/DotNetAge/goharness/config"
	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/session"
)

// newUsageTestCompactor 构造一个只关心记账的 compactor。
func newUsageTestCompactor(store session.TokenUsageStore) *compactor {
	rt := &Runtime{
		tokenUsageStore: store,
		model:           config.ModelConfig{Name: "test-model", Provider: "test-provider"},
		logger:          logging.NewNopLogger(),
	}
	return &compactor{rt: rt, logger: logging.NewNopLogger()}
}

// V-27：压缩那一次 LLM 调用的用量必须入账。
// 修复前 compactor.go 完全不引用 tokenUsageStore，压缩成本在账本里是 0。
func TestRecordCompactionUsage_WritesRecord(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryTokenUsageStore()
	c := newUsageTestCompactor(store)

	c.recordCompactionUsage(ctx, "sess-1", "agent-a", &gochatcore.Usage{
		PromptTokens:            235090,
		CompletionTokens:        5818,
		TotalTokens:             240908,
		PromptTokensDetails:     &gochatcore.PromptTokensDetails{CachedTokens: 196608},
		CompletionTokensDetails: &gochatcore.CompletionTokensDetails{ReasoningTokens: 0},
	})

	recs, err := store.Query(ctx, session.TokenUsageFilter{Kind: session.TokenUsageKindCompaction})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("期望 1 条压缩记录，实际 %d 条", len(recs))
	}

	r := recs[0]
	if r.Kind != session.TokenUsageKindCompaction {
		t.Errorf("Kind 不符：期望 %q，实际 %q", session.TokenUsageKindCompaction, r.Kind)
	}
	if r.SessionID != "sess-1" || r.AgentName != "agent-a" {
		t.Errorf("归属维度不符：session=%q agent=%q", r.SessionID, r.AgentName)
	}
	if r.ModelName != "test-model" || r.ProviderName != "test-provider" {
		t.Errorf("模型维度不符：model=%q provider=%q", r.ModelName, r.ProviderName)
	}
	if r.PromptTokens != 235090 || r.CompletionTokens != 5818 || r.TotalTokens != 240908 {
		t.Errorf("token 计数不符：prompt=%d completion=%d total=%d",
			r.PromptTokens, r.CompletionTokens, r.TotalTokens)
	}
	// 缓存命中量是最容易被漏掉的成本项
	if r.CachedTokens != 196608 {
		t.Errorf("CachedTokens 不符：期望 196608，实际 %d", r.CachedTokens)
	}
	if r.ID == "" {
		t.Error("ID 不应为空")
	}
}

// TotalTokens 缺失时用 prompt+completion 补齐，不能记成 0。
func TestRecordCompactionUsage_FillsTotalWhenMissing(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryTokenUsageStore()
	c := newUsageTestCompactor(store)

	c.recordCompactionUsage(ctx, "sess-2", "agent-a", &gochatcore.Usage{
		PromptTokens:     1000,
		CompletionTokens: 200,
	})

	recs, _ := store.Query(ctx, session.TokenUsageFilter{Kind: session.TokenUsageKindCompaction})
	if len(recs) != 1 {
		t.Fatalf("期望 1 条记录，实际 %d 条", len(recs))
	}
	if recs[0].TotalTokens != 1200 {
		t.Errorf("TotalTokens 应补齐为 1200，实际 %d", recs[0].TotalTokens)
	}
}

// nil usage 与 nil store 都必须安全跳过，不能 panic。
func TestRecordCompactionUsage_NilSafe(t *testing.T) {
	ctx := context.Background()

	c := newUsageTestCompactor(session.NewInMemoryTokenUsageStore())
	c.recordCompactionUsage(ctx, "s", "a", nil) // 不应 panic

	cNil := newUsageTestCompactor(nil)
	cNil.recordCompactionUsage(ctx, "s", "a", &gochatcore.Usage{PromptTokens: 1})
}

// Kind 过滤的三态语义：不过滤 / 只看主对话 / 只看压缩。
func TestTokenUsageFilter_KindSemantics(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryTokenUsageStore()

	if err := store.Append(ctx, session.TokenUsageRecord{ID: "main-1", SessionID: "s"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Append(ctx, session.TokenUsageRecord{
		ID: "comp-1", SessionID: "s", Kind: session.TokenUsageKindCompaction,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	all, _ := store.Query(ctx, session.TokenUsageFilter{SessionID: "s"})
	if len(all) != 2 {
		t.Errorf("不设 Kind 应返回全部 2 条，实际 %d 条", len(all))
	}

	main, _ := store.Query(ctx, session.TokenUsageFilter{Kind: session.TokenUsageKindMain})
	if len(main) != 1 || main[0].ID != "main-1" {
		t.Errorf("只看主对话应返回 main-1，实际 %+v", main)
	}

	comp, _ := store.Query(ctx, session.TokenUsageFilter{Kind: session.TokenUsageKindCompaction})
	if len(comp) != 1 || comp[0].ID != "comp-1" {
		t.Errorf("只看压缩应返回 comp-1，实际 %+v", comp)
	}
}
