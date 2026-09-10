package tools

import "encoding/json"

// MetaProvider 是工具向事件侧暴露结构化统计的可选接口。
//
// 背景：工具返回值会被 executor 序列化为字符串进入 LLM 上下文，而前端
// 渲染工具名片与轮级摘要所需的统计信息（±行数、命中数、退出码等）不应
// 要求前端重新解析这段文本（结果文本会被截断，且解析属于重复劳动）。
// 实现 MetaProvider 的工具可以把统计放在 ResultMeta() 里，由 executor
// 提取进 ToolExecutionResult.Metadata，最终进入 ToolExecEnd 事件的
// result_meta 字段——LLM 上下文完全不受污染。
type MetaProvider interface {
	ResultMeta() map[string]any
}

// MetaString 是「字符串结果 + 元数据旁路」的返回值形态。
// 序列化行为与裸 string 完全一致（LLM 看到的内容不变）。
type MetaString struct {
	Value string
	Meta  map[string]any
}

// MarshalJSON 让 MetaString 的序列化结果与裸 string 一致。
func (m MetaString) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.Value)
}

// ResultMeta 实现 MetaProvider 接口。
func (m MetaString) ResultMeta() map[string]any {
	return m.Meta
}

// MetaMap 是「map 结果 + 元数据旁路」的返回值形态。
// 序列化行为与裸 map 完全一致（LLM 看到的内容不变）。
type MetaMap struct {
	Data map[string]any
	Meta map[string]any
}

// MarshalJSON 让 MetaMap 的序列化结果与裸 map 一致。
func (m MetaMap) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.Data)
}

// ResultMeta 实现 MetaProvider 接口。
func (m MetaMap) ResultMeta() map[string]any {
	return m.Meta
}

// ExtractResultMeta 从工具返回值提取旁路元数据；未实现 MetaProvider 的
// 返回值返回 nil。供 executor 在构造 ToolExecutionResult 时调用。
func ExtractResultMeta(result any) map[string]any {
	if mp, ok := result.(MetaProvider); ok {
		return mp.ResultMeta()
	}
	return nil
}

// CountLines 统计文本行数（空串为 0，尾部换行不计入额外空行）。
func CountLines(s string) int {
	if s == "" {
		return 0
	}
	n := 1
	for _, r := range s {
		if r == '\n' {
			n++
		}
	}
	if s[len(s)-1] == '\n' {
		n--
	}
	return n
}
