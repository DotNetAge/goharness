package agents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/DotNetAge/goharness/tools"
)

type adapterMetaTool struct {
	val any
}

func (a *adapterMetaTool) Info() *tools.ToolInfo {
	return &tools.ToolInfo{Name: "AdapterMetaProbe"}
}
func (a *adapterMetaTool) Execute(context.Context, map[string]any) (any, error) { return a.val, nil }

// 回归：funcToolAdapter 的序列化分支也必须解包 MetaString。
// 该适配器当前不是主执行链路，但作为 goagent ToolCall 的公开包装，行为需与主链路一致。
func TestFuncToolAdapter_MetaStringIsUnwrapped(t *testing.T) {
	a := newFuncToolAdapter(&adapterMetaTool{val: tools.MetaString{Value: "line1\nline2"}})
	got, err := a.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "line1\nline2" {
		t.Fatalf("结果被 JSON 编码: %q", got)
	}
}

func TestFuncToolAdapter_NilMetaStringPointer(t *testing.T) {
	a := newFuncToolAdapter(&adapterMetaTool{val: (*tools.MetaString)(nil)})
	got, err := a.Execute(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("nil MetaString 应返回空串: %q", got)
	}
}
