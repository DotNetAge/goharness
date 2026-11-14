package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
	"github.com/DotNetAge/goharness/session"
)

// executorProbe 是同时验证 MetaString 解包与 ToolContext 合并的假工具。
type executorProbe struct {
	returnMeta bool
	envValue   string
}

func (p *executorProbe) Info() *ToolInfo {
	return &ToolInfo{Name: "ExecutorProbe", MaxResultSizeChars: -1}
}

func (p *executorProbe) Execute(ctx context.Context, _ map[string]any) (any, error) {
	if p.returnMeta {
		return MetaString{Value: "line1\nline2", Meta: map[string]any{"hit_count": 2}}, nil
	}
	tc := GetToolContext(ctx)
	if tc.SubProcessEnv == nil {
		return "NIL", nil
	}
	return tc.SubProcessEnv()["GOHARNESS_PROBE"], nil
}

// 回归：MetaString 必须原样进入 LLM 上下文，不得被 JSON 引号包裹。
func TestExecutor_MetaStringIsUnwrapped(t *testing.T) {
	reg := NewDefaultToolRegistry()
	if err := reg.Register(&executorProbe{returnMeta: true}); err != nil {
		t.Fatal(err)
	}
	exec := NewToolExecutor(reg, WithLogger(logging.NewNopLogger()))
	res, err := exec.Execute(context.Background(), "ExecutorProbe", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != "line1\nline2" {
		t.Fatalf("结果被额外编码: %q", res.Result)
	}
	if strings.HasPrefix(res.Result, `"`) || strings.Contains(res.Result, `\n`) {
		t.Fatalf("结果仍带 JSON 引号/转义: %q", res.Result)
	}
	meta, ok := res.Metadata.(map[string]any)
	if !ok || meta["hit_count"] != 2 {
		t.Fatalf("旁路元数据丢失: %v", res.Metadata)
	}
}

// 回归：执行器重建 ToolContext 时必须保留上游注入的 SubProcessEnv。
func TestExecutor_PreservesSubProcessEnv(t *testing.T) {
	reg := NewDefaultToolRegistry()
	if err := reg.Register(&executorProbe{}); err != nil {
		t.Fatal(err)
	}
	exec := NewToolExecutor(reg, WithLogger(logging.NewNopLogger()))
	ctx := WithToolContext(context.Background(), &ToolContext{
		SubProcessEnv: func() map[string]string { return map[string]string{"GOHARNESS_PROBE": "injected"} },
	})
	res, err := exec.Execute(ctx, "ExecutorProbe", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Result != "injected" {
		t.Fatalf("SubProcessEnv 未透传: %q", res.Result)
	}
}

// 端到端：SubProcessEnv 必须真正叠加到 Bash 子进程环境。
func TestExecutor_SubProcessEnvReachesBash(t *testing.T) {
	dir := t.TempDir()
	sb, err := sandbox.NewSandbox(&sandbox.SandboxPolicy{AllowedDirs: []string{dir}}, logging.NewNopLogger())
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.New("agent", "", dir, newMockSessionStore(), logging.NewNopLogger(), session.WithSandbox(sb))
	if err != nil {
		t.Fatal(err)
	}
	reg := NewDefaultToolRegistry()
	if err := reg.Register(NewBashTool()); err != nil {
		t.Fatal(err)
	}
	exec := NewToolExecutor(reg, WithSession(s), WithLogger(logging.NewNopLogger()))
	ctx := WithToolContext(context.Background(), &ToolContext{
		Session:       s,
		Logger:        logging.NewNopLogger(),
		SubProcessEnv: func() map[string]string { return map[string]string{"GOHARNESS_PROBE": "injected"} },
	})
	res, err := exec.Execute(ctx, "Bash", map[string]any{"command": `printf '%s' "$GOHARNESS_PROBE"`})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Result, "injected") {
		t.Fatalf("子进程未收到注入变量: %s", res.Result)
	}
}

// resultProbe 返回任意结果，用于覆盖执行器的序列化分支。
type resultProbe struct {
	name string
	val  any
	max  int
}

func (p *resultProbe) Info() *ToolInfo {
	return &ToolInfo{Name: p.name, MaxResultSizeChars: p.max}
}
func (p *resultProbe) Execute(context.Context, map[string]any) (any, error) { return p.val, nil }

func runProbe(t *testing.T, p *resultProbe) string {
	t.Helper()
	reg := NewDefaultToolRegistry()
	if err := reg.Register(p); err != nil {
		t.Fatal(err)
	}
	res, err := NewToolExecutor(reg, WithLogger(logging.NewNopLogger())).Execute(context.Background(), p.name, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Result
}

// MetaMap 的 JSON 语义不能被本次改动波及。
func TestExecutor_MetaMapUnchanged(t *testing.T) {
	got := runProbe(t, &resultProbe{name: "MetaMapProbe", val: MetaMap{
		Data: map[string]any{"stdout": "ok", "exit_code": 0},
	}, max: -1})
	if got != `{"exit_code":0,"stdout":"ok"}` {
		t.Fatalf("MetaMap 序列化变化: %q", got)
	}
}

// 指针形态与 nil 指针都要安全处理。
func TestExecutor_MetaStringPointerForms(t *testing.T) {
	got := runProbe(t, &resultProbe{name: "MetaPtrProbe", val: &MetaString{Value: "ptr"}, max: -1})
	if got != "ptr" {
		t.Fatalf("指针 MetaString 未解包: %q", got)
	}
	got = runProbe(t, &resultProbe{name: "MetaNilProbe", val: (*MetaString)(nil), max: -1})
	if got != "" {
		t.Fatalf("nil MetaString 应返回空串: %q", got)
	}
}

// 截断必须作用于 Value 本身，而不是 JSON 编码后的带引号文本。
func TestExecutor_MetaStringTruncationUsesValue(t *testing.T) {
	got := runProbe(t, &resultProbe{name: "MetaTruncProbe", val: MetaString{Value: "0123456789abcdef"}, max: 10})
	if !strings.HasPrefix(got, "0123456789") {
		t.Fatalf("截断未作用于 Value: %q", got)
	}
	if strings.HasPrefix(got, `"`) || strings.Contains(got, `\"`) {
		t.Fatalf("截断结果含 JSON 引号: %q", got)
	}
}

// FillMissingFrom：显式值优先，空字段才补齐。
func TestToolContext_FillMissingFromPrecedence(t *testing.T) {
	cfgEnv := func() map[string]string { return map[string]string{"k": "cfg"} }
	srcEnv := func() map[string]string { return map[string]string{"k": "src"} }
	dst := &ToolContext{SubProcessEnv: cfgEnv}
	dst.FillMissingFrom(&ToolContext{SubProcessEnv: srcEnv, Logger: logging.NewNopLogger()})
	if dst.SubProcessEnv()["k"] != "cfg" {
		t.Fatal("显式配置被上游覆盖")
	}
	if dst.Logger == nil {
		t.Fatal("空字段未被补齐")
	}
}
