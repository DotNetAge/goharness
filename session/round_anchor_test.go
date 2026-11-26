package session

import "testing"

// 工具产出的视觉附件以 user 角色传输（多模态协议要求），但它不是用户提问。
// 轮次切分必须用 IsRoundAnchor，否则视觉附件会被当成下一轮的起点：删轮时
// 在本轮中途截断，残留半截过程消息，甚至把真正的回答留在被删区间之外。
func TestGetRoundRangeIgnoresToolVisionAsBoundary(t *testing.T) {
	active := []Message{
		{Role: "user", Content: "帮我看看这张图", Timestamp: 100, Source: ""},
		{Role: "assistant", Content: "", Timestamp: 101, ToolCalls: []ToolCall{{ID: "c1", Name: "Read"}}},
		{Role: "tool", Content: "图片已读取", ToolCallID: "c1", Timestamp: 102},
		{Role: "user", Content: "[工具产出的图像] 工具 Read 读取到的图像如下", Timestamp: 103, Source: SourceToolVision},
		{Role: "assistant", Content: "图上是一个登录表单", Timestamp: 104},
		{Role: "user", Content: "那再改一下样式", Timestamp: 105},
		{Role: "assistant", Content: "好的", Timestamp: 106},
	}

	s := &Session{}
	roundEnd, err := s.getRoundRange(0, active)
	if err != nil {
		t.Fatalf("取轮范围失败: %v", err)
	}
	// 第一轮应包含视觉附件与它的回答，止于第二条真实提问（index 5）。
	if roundEnd != 5 {
		t.Fatalf("轮边界应止于真实提问（index 5），实际得到 %d", roundEnd)
	}

	// 视觉附件本身不能作为轮锚点：从它出发取轮必须报错，而不是当成新轮开头。
	if _, err := s.getRoundRange(3, active); err == nil {
		t.Fatalf("视觉附件不应被当作轮次起点")
	}

	// 第二轮（index 5）应正常取到末尾。
	roundEnd, err = s.getRoundRange(5, active)
	if err != nil {
		t.Fatalf("取第二轮失败: %v", err)
	}
	if roundEnd != len(active) {
		t.Fatalf("第二轮应到窗口末尾 %d，实际 %d", len(active), roundEnd)
	}
}

// 同一秒内视觉附件与真实提问时间戳相同：按时间戳定位轮次必须命中真实提问，
// 不能命中视觉附件。
func TestFindMessageIndexByTimestampSkipsToolVision(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "提问一", Timestamp: 200},
		{Role: "tool", Content: "结果", Timestamp: 201, ToolCallID: "c1"},
		{Role: "user", Content: "[工具产出的图像] …", Timestamp: 202, Source: SourceToolVision},
		{Role: "assistant", Content: "回答", Timestamp: 202},
		{Role: "user", Content: "提问二", Timestamp: 203},
	}
	if idx := findMessageIndexByTimestamp(msgs, 202); idx < 0 || msgs[idx].Source == SourceToolVision {
		// 202 上没有真实提问：兜底可以命中同秒的其它消息（assistant），但绝不能
		// 落在视觉附件上，否则截断会切在轮次中间。
		t.Fatalf("202 不应命中视觉附件，实际 index %d", idx)
	}
	if idx := findMessageIndexByTimestamp(msgs, 203); idx != 4 {
		t.Fatalf("应命中真实提问 index 4，实际 %d", idx)
	}
}
