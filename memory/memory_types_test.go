package memory

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 测试必须包含 10 秒超时（项目硬性约束）。
func testCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func TestFormatMemoryRecords(t *testing.T) {
	ctx, cancel := testCtx()
	defer cancel()
	_ = ctx

	ts := time.Date(2026, 8, 2, 15, 4, 0, 0, time.Local)

	tests := []struct {
		name   string
		chunk  MemoryChunk
		expect string
	}{
		{
			name: "普通文本：内容前加 - 直接输出",
			chunk: MemoryChunk{
				Title:     "标题",
				Content:   "今天天气很好",
				Tags:      []string{"t1", "t2"},
				Timestamp: ts,
			},
			expect: "- [2026-08-02 15:04] 标题 - 今天天气很好 。标签:[t1, t2]",
		},
		{
			name: "无序列表：逐行缩进为子列表",
			chunk: MemoryChunk{
				Title:     "标题",
				Content:   "- 第一项\n- 第二项",
				Tags:      []string{"t1"},
				Timestamp: ts,
			},
			expect: "- [2026-08-02 15:04] 标题\n  - 第一项\n  - 第二项\n   。标签:[t1]",
		},
		{
			name: "有序列表：数字加点识别为列表",
			chunk: MemoryChunk{
				Title:     "标题",
				Content:   "1. 步骤一\n2. 步骤二",
				Tags:      nil,
				Timestamp: ts,
			},
			expect: "- [2026-08-02 15:04] 标题\n  1. 步骤一\n  2. 步骤二",
		},
		{
			name: "混合文本：含非列表行按普通文本处理",
			chunk: MemoryChunk{
				Title:     "标题",
				Content:   "- 第一项\n普通说明",
				Tags:      nil,
				Timestamp: ts,
			},
			expect: "- [2026-08-02 15:04] 标题 - - 第一项\n  普通说明",
		},
		{
			name: "无标题无标签：仅输出时间与内容",
			chunk: MemoryChunk{
				Content:   "纯内容",
				Timestamp: ts,
			},
			expect: "- [2026-08-02 15:04]  - 纯内容",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatMemoryRecords([]MemoryChunk{tt.chunk})
			if got != tt.expect {
				t.Errorf("FormatMemoryRecords() = %q, 期望 %q", got, tt.expect)
			}
		})
	}
}

func TestIsMarkdownList(t *testing.T) {
	ctx, cancel := testCtx()
	defer cancel()
	_ = ctx

	tests := []struct {
		name    string
		content string
		expect  bool
	}{
		{"无序列表", "- 第一项\n- 第二项", true},
		{"无序星号", "* 第一项", true},
		{"无序加号", "+ 第一项", true},
		{"有序列表", "1. 第一项\n2. 第二项", true},
		{"有序括号", "1) 第一项", true},
		{"普通文本", "今天天气很好", false},
		{"混入普通行", "- 第一项\n普通说明", false},
		{"空内容", "", false},
		{"带缩进的列表", "  - 第一项\n    - 第二项", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMarkdownList(tt.content); got != tt.expect {
				t.Errorf("isMarkdownList(%q) = %v, 期望 %v", tt.content, got, tt.expect)
			}
		})
	}
}

// 确保格式化结果可读且不含多余空行。
func TestFormatMemoryRecordsNoBlankLines(t *testing.T) {
	ctx, cancel := testCtx()
	defer cancel()
	_ = ctx

	chunks := []MemoryChunk{
		{Title: "A", Content: "- 第一项\n- 第二项", Tags: []string{"t"}, Timestamp: time.Now()},
		{Title: "B", Content: "普通文本", Timestamp: time.Now()},
	}
	got := FormatMemoryRecords(chunks)
	if strings.Contains(got, "\n\n") {
		t.Errorf("输出包含多余空行: %q", got)
	}
}
