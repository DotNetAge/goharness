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

// 挂起提问的降级回答（写入闸门把 AskUser 应答退回 user 消息）属于当前轮，
// 不是新提问：否则刷新后会凭空多出一轮，用量与删轮定位都跟着错。
func TestGetRoundRangeIgnoresAskAnswerAsBoundary(t *testing.T) {
	active := []Message{
		{Role: "user", Content: "帮我看看这个方案", Timestamp: 300},
		{Role: "assistant", Content: "", Timestamp: 301, ToolCalls: []ToolCall{{ID: "a1", Name: "AskUser"}}},
		{Role: "user", Content: "选项 A", Timestamp: 302, Source: SourceAskAnswer},
		{Role: "assistant", Content: "好的，按 A 来", Timestamp: 303},
		{Role: "user", Content: "再确认一下", Timestamp: 304},
	}

	s := &Session{}
	roundEnd, err := s.getRoundRange(0, active)
	if err != nil {
		t.Fatalf("取轮范围失败: %v", err)
	}
	if roundEnd != 4 {
		t.Fatalf("降级回答不能切轮：应止于 index 4，实际 %d", roundEnd)
	}
	if _, err := s.getRoundRange(2, active); err == nil {
		t.Fatal("降级回答不应被当作轮次起点")
	}
}

// 改动前落盘的旧格式视觉注入（无来源标记、正文是旧文案、带图）同样不是用户发言：
// 否则历史会话刷新后会多切一轮，撤销轮次还会截在轮中间。
func TestGetRoundRangeIgnoresLegacyVisionInjection(t *testing.T) {
	legacy := Message{
		Role:      "user",
		Content:   "以下是工具 Read 读取到的图片内容（视觉消息），请结合图片进行分析：",
		Images:    []ImageBlock{{MediaType: "image/jpeg", Base64Data: "AAAA"}},
		Timestamp: 400,
	}
	if legacy.IsRoundAnchor() {
		t.Fatal("旧格式视觉注入不应被当作轮次边界")
	}
	if !legacy.IsLegacyVisionInjection() {
		t.Fatal("旧格式视觉注入应被识别")
	}

	// 用户自己发的图（同样是 user + 带图，但正文是用户写的）不能被误判。
	userImage := Message{
		Role: "user", Content: "看看这个界面哪里不对", Timestamp: 401,
		Images: []ImageBlock{{MediaType: "image/jpeg", Path: "/tmp/shot.jpg"}},
	}
	if !userImage.IsRoundAnchor() {
		t.Fatal("用户自己发图仍是真实提问")
	}
	if userImage.IsLegacyVisionInjection() {
		t.Fatal("用户发的图不应被识别成旧格式注入")
	}

	active := []Message{
		{Role: "user", Content: "第一问", Timestamp: 399},
		legacy,
		{Role: "assistant", Content: "图里是登录页", Timestamp: 402},
		{Role: "user", Content: "第二问", Timestamp: 403},
	}
	s := &Session{}
	roundEnd, err := s.getRoundRange(0, active)
	if err != nil {
		t.Fatalf("取轮范围失败: %v", err)
	}
	if roundEnd != 3 {
		t.Fatalf("旧格式注入不能切轮：应止于 index 3，实际 %d", roundEnd)
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
