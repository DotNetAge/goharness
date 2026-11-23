package tools

import "testing"

// TestBuiltinToolSchemasHaveArrayItems 是 schema 回归闸门：
// JSON Schema 的 array 必须声明 items，否则模型会自由决定元素形状。
func TestBuiltinToolSchemasHaveArrayItems(t *testing.T) {
	toolList := []FuncTool{
		NewGrepTool(),
		NewRunScriptTool(),
		NewAskUserTool(),
		NewTaskUpdateTool(),
		NewTaskListTool(),
		NewTeamCreateTool(),
		NewCollectResultsTool(nil),
	}
	for _, tool := range toolList {
		info := tool.Info()
		for _, p := range info.Parameters {
			if p.Type != "array" {
				continue
			}
			if p.Items == nil || p.Items.Type == "" {
				t.Errorf("%s.%s: array 参数缺少 items schema", info.Name, p.Name)
			}
		}
	}
}
