package tools

import "testing"

// unpackResultMap 将工具返回值解包为 map，兼容 MetaMap 旁路包装。
func unpackResultMap(t *testing.T, result any) map[string]any {
	t.Helper()
	if mm, ok := result.(MetaMap); ok {
		return mm.Data
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("期望 map 结果，实际 %T", result)
	}
	return m
}

// unpackResultString 将工具返回值解包为 string，兼容 MetaString 旁路包装。
func unpackResultString(t *testing.T, result any) string {
	t.Helper()
	if ms, ok := result.(MetaString); ok {
		return ms.Value
	}
	s, ok := result.(string)
	if !ok {
		t.Fatalf("期望 string 结果，实际 %T", result)
	}
	return s
}
