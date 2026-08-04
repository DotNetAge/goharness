package loop

import (
	"testing"

	"github.com/DotNetAge/goharness/hooks"
	"github.com/DotNetAge/goharness/logging"
)

func TestDefaults_ReturnsExpectedHooks(t *testing.T) {
	hooksList := Defaults(logging.NewNopLogger())
	if len(hooksList) != 2 {
		t.Fatalf("Defaults 应返回 2 个 hook, got %d", len(hooksList))
	}

	// 第一个应为 LoopLoggerHook
	if _, ok := hooksList[0].(*LoopLoggerHook); !ok {
		t.Errorf("第一个 hook 应为 *LoopLoggerHook, got %T", hooksList[0])
	}
	// 第二个应为 ConvergenceHook
	if _, ok := hooksList[1].(*ConvergenceHook); !ok {
		t.Errorf("第二个 hook 应为 *ConvergenceHook, got %T", hooksList[1])
	}
}

func TestDefaults_PriorityOrder(t *testing.T) {
	hooksList := Defaults(logging.NewNopLogger())
	if len(hooksList) < 2 {
		t.Fatal("至少需要 2 个 hook")
	}
	// 优先级应递增（lower = earlier execution）
	for i := 1; i < len(hooksList); i++ {
		if hooksList[i].Priority() < hooksList[i-1].Priority() {
			t.Errorf("优先级顺序错误: hooks[%d].Priority()=%d < hooks[%d].Priority()=%d",
				i, hooksList[i].Priority(), i-1, hooksList[i-1].Priority())
		}
	}
}

func TestDefaults_NilLogger(t *testing.T) {
	// nil logger 不应 panic（LoopLoggerHook 会检查 Logger == nil）
	hooksList := Defaults(nil)
	if len(hooksList) != 2 {
		t.Fatalf("nil logger 时仍应返回 2 个 hook, got %d", len(hooksList))
	}
}

func TestDefaults_ImplementsLoopHook(t *testing.T) {
	hooksList := Defaults(logging.NewNopLogger())
	for i, h := range hooksList {
		// 确保所有返回的 hook 都实现 LoopHook 接口
		_ = h.Priority()
		_ = h.BeforeLLM("s", 0, &hooks.CallInput{})
		_ = h.AfterLLM("s", 0, &hooks.LLMResponse{}, nil)
		h.Abort("s", "测试")
		// 如果不实现接口，上面调用会编译失败
		_ = i
	}
}
