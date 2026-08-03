package agents

import (
	"testing"
	"time"
)

// ─── parseCompactionResponse JSON 解析测试 ─────────────────────────

func TestParseCompactionResponse_EmptyText(t *testing.T) {
	chunks, err := parseCompactionResponse("")
	if err != nil {
		t.Errorf("空文本不应返回错误: %v", err)
	}
	if chunks != nil {
		t.Errorf("空文本应返回 nil，得到 %v", chunks)
	}
}

func TestParseCompactionResponse_ValidSingleChunk(t *testing.T) {
	response := `[{"summary":"测试摘要","content":"测试内容","tags":["test"]}]`
	chunks, err := parseCompactionResponse(response)
	if err != nil {
		t.Fatalf("parseCompactionResponse 失败: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("应返回 1 个 chunk，得到 %d", len(chunks))
	}
	if chunks[0].Summary != "测试摘要" {
		t.Errorf("Summary = %q, want %q", chunks[0].Summary, "测试摘要")
	}
	if chunks[0].Content != "测试内容" {
		t.Errorf("Content = %q, want %q", chunks[0].Content, "测试内容")
	}
}

func TestParseCompactionResponse_MarkdownWrapped(t *testing.T) {
	// LLM 返回被 markdown 代码块包裹的 JSON
	response := "```json\n[{\"summary\":\"摘要\",\"content\":\"内容\",\"tags\":[]}]\n```"
	chunks, err := parseCompactionResponse(response)
	if err != nil {
		t.Fatalf("parseCompactionResponse 失败: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("应返回 1 个 chunk，得到 %d", len(chunks))
	}
	if chunks[0].Content != "内容" {
		t.Errorf("Content = %q, want %q", chunks[0].Content, "内容")
	}
}

func TestParseCompactionResponse_EmptyArray(t *testing.T) {
	// LLM 判定无实质信息，返回空数组
	chunks, err := parseCompactionResponse("[]")
	if err != nil {
		t.Errorf("空数组不应返回错误: %v", err)
	}
	if chunks != nil {
		t.Errorf("空数组应返回 nil，得到 %v", chunks)
	}
}

func TestParseCompactionResponse_EmptyObject(t *testing.T) {
	// LLM 返回空对象
	chunks, err := parseCompactionResponse("{}")
	if err != nil {
		t.Errorf("空对象不应返回错误: %v", err)
	}
	if chunks != nil {
		t.Errorf("空对象应返回 nil，得到 %v", chunks)
	}
}

func TestParseCompactionResponse_FilterEmptyContent(t *testing.T) {
	// content 为空的 chunk 应被过滤
	response := `[{"summary":"有内容","content":"实际内容","tags":[]},{"summary":"空内容","content":"","tags":[]}]`
	chunks, err := parseCompactionResponse(response)
	if err != nil {
		t.Fatalf("parseCompactionResponse 失败: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("应过滤掉空 content 的 chunk，得到 %d 个", len(chunks))
	}
	if chunks[0].Content != "实际内容" {
		t.Errorf("Content = %q, want %q", chunks[0].Content, "实际内容")
	}
}

func TestParseCompactionResponse_InvalidJSON(t *testing.T) {
	// 非 JSON 文本应返回错误
	_, err := parseCompactionResponse("this is not json at all")
	if err == nil {
		t.Error("非 JSON 文本应返回错误")
	}
}

func TestParseCompactionResponse_TagsNilDefault(t *testing.T) {
	// LLM 未填 tags 字段时，应默认为空切片
	response := `[{"summary":"摘要","content":"内容"}]`
	chunks, err := parseCompactionResponse(response)
	if err != nil {
		t.Fatalf("parseCompactionResponse 失败: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("应返回 1 个 chunk，得到 %d", len(chunks))
	}
	if chunks[0].Tags == nil {
		t.Error("Tags 为 nil 时应默认为空切片")
	}
	if len(chunks[0].Tags) != 0 {
		t.Errorf("Tags 应为空切片，得到 %v", chunks[0].Tags)
	}
}

// ─── 时间戳解析测试 ─────────────────────────

func TestParseCompactionResponse_TimestampParsed(t *testing.T) {
	response := `[{"summary":"测试摘要","content":"测试内容","tags":["test"],"timestamp":"2026-07-02T14:30:45Z"}]`
	chunks, err := parseCompactionResponse(response)
	if err != nil {
		t.Fatalf("parseCompactionResponse 失败: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("应返回 1 个 chunk，得到 %d", len(chunks))
	}
	want, _ := time.Parse(time.RFC3339, "2026-07-02T14:30:45Z")
	if !chunks[0].Timestamp.Equal(want) {
		t.Errorf("Timestamp = %v, want %v", chunks[0].Timestamp, want)
	}
}

func TestParseCompactionResponse_TimestampMissing(t *testing.T) {
	// LLM 没填 timestamp 字段
	response := `[{"summary":"测试摘要","content":"测试内容","tags":["test"]}]`
	chunks, err := parseCompactionResponse(response)
	if err != nil {
		t.Fatalf("parseCompactionResponse 失败: %v", err)
	}
	if !chunks[0].Timestamp.IsZero() {
		t.Errorf("LLM 未填 timestamp 时应保持零值，得到 %v", chunks[0].Timestamp)
	}
}

func TestParseCompactionResponse_TimestampInvalid(t *testing.T) {
	// LLM 填了非法格式的时间戳
	response := `[{"summary":"测试摘要","content":"测试内容","tags":["test"],"timestamp":"not-a-date"}]`
	chunks, err := parseCompactionResponse(response)
	if err != nil {
		t.Fatalf("parseCompactionResponse 失败: %v", err)
	}
	if !chunks[0].Timestamp.IsZero() {
		t.Errorf("timestamp 解析失败时应保持零值，得到 %v", chunks[0].Timestamp)
	}
}
