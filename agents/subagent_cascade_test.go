package agents

import (
	"context"
	"testing"
	"time"
)

// 团队级联急停：在主会话点一次停止，必须停掉整棵派生树。
//
// 修复前 cancelSponsored 只处理直接子级：负责人 → 成员可以停，
// 但成员再派工的孙代理登记在成员会话名下，不会被取消，
// 用户只能逐个打开成员会话分别停止。
func TestCancelSponsored_RecursesWholeTree(t *testing.T) {
	m := newPruneTestManager()

	var stopped []string
	mk := func(name string) context.CancelFunc {
		return func() { stopped = append(stopped, name) }
	}

	m.mu.Lock()
	// 负责人 → 成员 A / 成员 B
	m.sponsored["boss"] = map[string]context.CancelFunc{
		"member-a": mk("member-a"),
		"member-b": mk("member-b"),
	}
	// 成员 A 又派了两个孙代理
	m.sponsored["member-a"] = map[string]context.CancelFunc{
		"grand-a1": mk("grand-a1"),
		"grand-a2": mk("grand-a2"),
	}
	// 成员 B 派一个
	m.sponsored["member-b"] = map[string]context.CancelFunc{
		"grand-b1": mk("grand-b1"),
	}
	m.mu.Unlock()

	n := m.cancelSponsored("boss")

	want := 5 // member-a, member-b, grand-a1, grand-a2, grand-b1
	if n != want {
		t.Fatalf("应强停整棵树 %d 个，实际 %d 个（stopped=%v）", want, n, stopped)
	}
	if len(stopped) != want {
		t.Errorf("实际调用 cancel 次数不符：期望 %d，实际 %d", want, len(stopped))
	}

	// 登记应被清空，避免重复强停
	m.mu.Lock()
	left := len(m.sponsored)
	m.mu.Unlock()
	if left != 0 {
		t.Errorf("强停后 sponsored 应清空，残留 %d 项", left)
	}
}

// 环状登记（理论上不该出现，但防御性处理）：不能死循环。
func TestCancelSponsored_CycleSafe(t *testing.T) {
	m := newPruneTestManager()
	noop := func() {}

	m.mu.Lock()
	m.sponsored["a"] = map[string]context.CancelFunc{"b": noop}
	m.sponsored["b"] = map[string]context.CancelFunc{"a": noop}
	m.mu.Unlock()

	done := make(chan int, 1)
	go func() { done <- m.cancelSponsored("a") }()

	select {
	case n := <-done:
		if n != 2 {
			t.Errorf("环状登记应各取消一次，实际 %d 次", n)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelSponsored 在环状登记下死循环")
	}
}
