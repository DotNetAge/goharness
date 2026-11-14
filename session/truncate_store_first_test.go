package session

import (
	"context"
	"errors"
	"testing"

	"github.com/DotNetAge/goharness/logging"
)

// V-17：Truncate 必须「存储优先」。
//
// 反例（修复前）：先改内存、再写存储。store 失败时返回错误，但内存切片已经
// 截断、尾部消息不可恢复，造成内存与磁盘不一致，且调用方无从回滚。
// 期望：store 失败时内存与 cursor 都不变。
func TestTruncate_StoreFailureLeavesMemoryIntact(t *testing.T) {
	ctx := context.Background()
	store := newMockStore()
	s, err := New("agent", "sponsor", "/tmp/proj", store, logging.NewNopLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := s.Append(ctx,
		Message{Role: "user", Content: "m1"},
		Message{Role: "assistant", Content: "m2"},
		Message{Role: "user", Content: "m3"},
	); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if got := len(s.All()); got != 3 {
		t.Fatalf("前置条件失败：期望 3 条消息，实际 %d", got)
	}

	store.truncateErr = errors.New("disk full")

	if err := s.Truncate(ctx, 1); err == nil {
		t.Fatal("store.Truncate 失败时 Truncate 必须返回错误")
	}
	if got := len(s.All()); got != 3 {
		t.Errorf("store 失败后内存不应被截断：期望 3 条，实际 %d 条", got)
	}
	if got := s.Cursor(); got != 0 {
		t.Errorf("store 失败后 cursor 不应变化：期望 0，实际 %d", got)
	}
}

// store 成功时才允许改动内存。
func TestTruncate_StoreOKThenTrimsMemory(t *testing.T) {
	ctx := context.Background()
	store := newMockStore()
	s, err := New("agent", "sponsor", "/tmp/proj", store, logging.NewNopLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Append(ctx,
		Message{Role: "user", Content: "m1"},
		Message{Role: "assistant", Content: "m2"},
		Message{Role: "user", Content: "m3"},
	); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if err := s.Truncate(ctx, 1); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	if got := len(s.All()); got != 1 {
		t.Errorf("期望保留 1 条，实际 %d 条", got)
	}
}

// keepCount >= 现有条数时是 no-op，不应触碰存储。
func TestTruncate_NoopWhenKeepCountBeyondLength(t *testing.T) {
	ctx := context.Background()
	store := newMockStore()
	s, err := New("agent", "sponsor", "/tmp/proj", store, logging.NewNopLogger())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Append(ctx, Message{Role: "user", Content: "m1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	store.truncateErr = errors.New("不应被调用")
	if err := s.Truncate(ctx, 5); err != nil {
		t.Errorf("keepCount 超过条数时应直接返回 nil，实际错误：%v", err)
	}
	if got := len(s.All()); got != 1 {
		t.Errorf("期望 1 条，实际 %d 条", got)
	}
}
