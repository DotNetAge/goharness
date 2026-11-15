package tools

import "testing"

// 状态机语义取自 validTransitions（tasks.go）：
//   pending    → in_progress / completed / cancelled
//   in_progress → completed / cancelled
//   completed / cancelled 为终态，不可再迁移。
// 未定义的当前状态一律拒绝。

// TestValidTaskTransition_AllPairs 全矩阵验证：对全部 4×4 组合断言允许/拒绝，
// 任何一条不符合 validTransitions 定义即为状态机漏洞。
func TestValidTaskTransition_AllPairs(t *testing.T) {
	all := []TaskStatus{TaskPending, TaskInProgress, TaskCompleted, TaskCancelled}

	allowed := map[TaskStatus]map[TaskStatus]bool{
		TaskPending: {
			TaskInProgress: true, TaskCompleted: true, TaskCancelled: true,
			TaskPending: false, // pending → pending 无意义，不在允许列表
		},
		TaskInProgress: {
			TaskCompleted: true, TaskCancelled: true,
			TaskPending: false, // 不允许回退到 pending
			TaskInProgress: false,
		},
		TaskCompleted: { // 终态
			TaskPending: false, TaskInProgress: false, TaskCompleted: false, TaskCancelled: false,
		},
		TaskCancelled: { // 终态
			TaskPending: false, TaskInProgress: false, TaskCompleted: false, TaskCancelled: false,
		},
	}

	for _, cur := range all {
		for _, next := range all {
			got := ValidTaskTransition(cur, next)
			want := allowed[cur][next]
			if got != want {
				t.Errorf("ValidTaskTransition(%s → %s) = %v，期望 %v", cur, next, got, want)
			}
		}
	}
}

// TestValidTaskTransition_UnknownStatus 未定义的当前状态（空串、拼错值）
// 必须拒绝，不得 panic 或误放行。
func TestValidTaskTransition_UnknownStatus(t *testing.T) {
	cases := []struct {
		current TaskStatus
		next    TaskStatus
	}{
		{"", TaskInProgress},
		{"", TaskCompleted},
		{"running", TaskCompleted},  // 拼错值
		{"Pending", TaskInProgress}, // 大小写不匹配（状态值全小写）
	}
	for _, c := range cases {
		if got := ValidTaskTransition(c.current, c.next); got {
			t.Errorf("未知当前状态 %q 迁移到 %q 应拒绝，实际放行", c.current, c.next)
		}
	}
}
