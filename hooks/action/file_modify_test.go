package action

import (
	"errors"
	"testing"
)

// ── FileModifyHook tests ──────────────────────────────────────────────────

func TestFileModifyHook_Priority(t *testing.T) {
	hook := NewFileModifyHook(nil, nil)
	if hook.Priority() != PriorityFileModify {
		t.Errorf("Priority = %d, want %d", hook.Priority(), PriorityFileModify)
	}
}

func TestFileModifyHook_Before_FileModifyingTool(t *testing.T) {
	var trackedPath string
	tracker := func(path string) error {
		trackedPath = path
		return nil
	}

	hook := NewFileModifyHook(func(sessionID string) (TrackFunc, bool) {
		return tracker, true
	}, nil)

	result := hook.Before("sess-1", "Write", map[string]any{"path": "/tmp/test.go"})
	if result.Error != nil {
		t.Fatalf("不应返回错误: %v", result.Error)
	}
	if trackedPath != "/tmp/test.go" {
		t.Errorf("追踪路径 = %q, want %q", trackedPath, "/tmp/test.go")
	}
}

func TestFileModifyHook_Before_NonModifyingTool(t *testing.T) {
	called := false
	hook := NewFileModifyHook(func(sessionID string) (TrackFunc, bool) {
		return func(p string) error { called = true; return nil }, true
	}, nil)

	hook.Before("sess-1", "Read", map[string]any{"path": "/tmp/test.go"})
	if called {
		t.Error("非文件修改工具不应调用 tracker")
	}
}

func TestFileModifyHook_Before_NoTrackerProvider(t *testing.T) {
	hook := NewFileModifyHook(nil, nil)

	result := hook.Before("sess-1", "Write", map[string]any{"path": "/tmp/test.go"})
	if result.Error != nil || result.Abort {
		t.Errorf("无 provider 时应跳过: Abort=%v Error=%v", result.Abort, result.Error)
	}
}

func TestFileModifyHook_Before_NoTrackerForSession(t *testing.T) {
	hook := NewFileModifyHook(func(sessionID string) (TrackFunc, bool) {
		return nil, false // 该 session 无 tracker
	}, nil)

	result := hook.Before("sess-unknown", "Write", map[string]any{"path": "/tmp/test.go"})
	if result.Error != nil || result.Abort {
		t.Errorf("session 无 tracker 时应跳过: %+v", result)
	}
}

func TestFileModifyHook_Before_EmptyFilePath(t *testing.T) {
	called := false
	hook := NewFileModifyHook(func(sessionID string) (TrackFunc, bool) {
		return func(p string) error { called = true; return nil }, true
	}, nil)

	hook.Before("sess-1", "Write", map[string]any{"other_key": "value"})
	if called {
		t.Error("空文件路径不应调用 tracker")
	}
}

func TestFileModifyHook_Before_TrackerError(t *testing.T) {
	expectedErr := errors.New("backup failed")
	hook := NewFileModifyHook(func(sessionID string) (TrackFunc, bool) {
		return func(p string) error { return expectedErr }, true
	}, nil)

	result := hook.Before("sess-1", "Write", map[string]any{"path": "/tmp/test.go"})
	if result.Error == nil {
		t.Fatal("tracker 返回错误时 HookResult 应包含 error")
	}
	// 错误不应导致 abort（追踪失败不阻止执行）
	if result.Abort {
		t.Error("追踪失败不应设置 Abort")
	}
}

func TestFileModifyHook_After_NoOp(t *testing.T) {
	hook := NewFileModifyHook(nil, nil)
	result := hook.After(nil)
	if result.Abort || result.Error != nil {
		t.Errorf("After 应为 no-op: %+v", result)
	}
}

func TestFileModifyHook_Abort_NoOp(t *testing.T) {
	hook := NewFileModifyHook(nil, nil)
	// 不应 panic
	hook.Abort("test reason")
}

func TestExtractFilePath(t *testing.T) {
	tests := []struct {
		name     string
		params   map[string]any
		expected string
	}{
		{"path key", map[string]any{"path": "/a/b.go"}, "/a/b.go"},
		{"file_path key", map[string]any{"file_path": "/c/d.go"}, "/c/d.go"},
		{"filepath key", map[string]any{"filepath": "/e/f.go"}, "/e/f.go"},
		{"empty value", map[string]any{"path": ""}, ""},
		{"no match", map[string]any{"other": "value"}, ""},
		{"nil params", nil, ""},
		{"first key wins", map[string]any{"path": "/first.go", "file_path": "/second.go"}, "/first.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilePath(tt.params)
			if got != tt.expected {
				t.Errorf("extractFilePath() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFileModifyingTools_Set(t *testing.T) {
	// 验证 Write 和 FileEdit 在集合中
	for _, name := range []string{"Write", "Edit"} {
		if !fileModifyingTools[name] {
			t.Errorf("工具 %q 应在 fileModifyingTools 集合中", name)
		}
	}
	// 验证 Read 不在集合中
	if fileModifyingTools["Read"] {
		t.Error("Read 不应在 fileModifyingTools 集合中")
	}
}

// ── Defaults with TrackerProvider tests ───────────────────────────────────

func TestDefaults_WithTrackerProvider(t *testing.T) {
	provider := func(sessionID string) (TrackFunc, bool) {
		return func(p string) error { return nil }, true
	}

	hooks := Defaults(nil, nil, nil, provider)

	// 应包含 PermissionHook + FileModifyHook + ToolLoggerHook = 3 个
	if len(hooks) != 3 {
		t.Fatalf("期望 3 个 hooks，得到 %d", len(hooks))
	}

	// 验证优先级顺序：Permission(41) < FileModify(42) < Logger(46)
	if hooks[0].Priority() != 41 {
		t.Errorf("hooks[0].Priority() = %d, want 41", hooks[0].Priority())
	}
	if _, ok := hooks[1].(*FileModifyHook); !ok {
		t.Error("hooks[1] 应为 FileModifyHook")
	}
	if hooks[1].Priority() != 42 {
		t.Errorf("hooks[1].Priority() = %d, want 42", hooks[1].Priority())
	}
	if hooks[2].Priority() != 46 {
		t.Errorf("hooks[2].Priority() = %d, want 46", hooks[2].Priority())
	}
}

func TestDefaults_WithoutTrackerProvider(t *testing.T) {
	hooks := Defaults(nil, nil, nil)

	// FileModifyHook 始终被注册（即使 provider 为 nil）
	if len(hooks) != 3 {
		t.Fatalf("期望 3 个 hooks（PermissionHook + FileModifyHook + ToolLoggerHook），得到 %d", len(hooks))
	}

	// 确认 FileModifyHook 存在，但 provider 为 nil
	found := false
	for _, h := range hooks {
		if fmh, ok := h.(*FileModifyHook); ok {
			found = true
			if fmh.trackerProvider != nil {
				t.Error("未提供 tracker 时 FileModifyHook 的 provider 应为 nil")
			}
		}
	}
	if !found {
		t.Error("应包含 FileModifyHook（即使 provider 为 nil）")
	}
}

func TestDefaults_NilTrackerProvider(t *testing.T) {
	hooks := Defaults(nil, nil, nil, nil)

	// 显式传 nil 也应注册 FileModifyHook（provider 为 nil）
	if len(hooks) != 3 {
		t.Fatalf("期望 3 个 hooks，得到 %d", len(hooks))
	}

	// 确认 FileModifyHook 存在
	found := false
	for _, h := range hooks {
		if _, ok := h.(*FileModifyHook); ok {
			found = true
			break
		}
	}
	if !found {
		t.Error("nil provider 时也应包含 FileModifyHook")
	}
}
