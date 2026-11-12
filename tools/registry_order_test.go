package tools

import (
	"context"
	"reflect"
	"testing"
)

// stableOrderStub 是仅用于验证注册表顺序稳定性的最小 FuncTool 实现。
type stableOrderStub struct{ name string }

func (s stableOrderStub) Info() *ToolInfo { return &ToolInfo{Name: s.name} }

func (s stableOrderStub) Execute(_ context.Context, _ map[string]any) (any, error) {
	return nil, nil
}

// TestDefaultToolRegistry_AllStableOrder 守护 LLM prefix cache 前缀稳定性。
//
// tools 定义数组是请求体前缀的一部分（system -> tools -> messages）。若 All()
// 按 map 迭代顺序返回，每次新建 Run 重新装配工具适配器时顺序都会抖动，
// provider 认不出上一 Run 的前缀，整段会话历史按未命中全价重算。
// 该用例是对应回归护栏：顺序不但要稳定，还必须是确定的字典序。
func TestDefaultToolRegistry_AllStableOrder(t *testing.T) {
	r := NewDefaultToolRegistry()
	names := []string{
		"Bash", "Read", "Write", "Edit", "Glob", "Grep", "AskUser", "SubAgent",
		"TeamList", "Skill", "Cron", "WebFetch", "Ls", "TodoWrite", "WebSearch",
		"AgentGet", "Harvest", "Permission", "Session", "NotebookEdit",
	}
	for _, n := range names {
		if err := r.Register(stableOrderStub{name: n}); err != nil {
			t.Fatalf("注册 %q 失败: %v", n, err)
		}
	}

	var want []string
	for _, tool := range r.All() {
		want = append(want, tool.Info().Name)
	}

	for i := 0; i < 50; i++ {
		var got []string
		for _, tool := range r.All() {
			got = append(got, tool.Info().Name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("第 %d 次 All() 顺序抖动:\n want %v\n got  %v", i, want, got)
		}
	}

	for i := 1; i < len(want); i++ {
		if want[i-1] >= want[i] {
			t.Fatalf("All() 未按工具名排序: %v", want)
		}
	}
}
