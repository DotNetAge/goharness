package agents

import (
	"testing"

	"github.com/DotNetAge/goharness/logging"
)

// TestFailedTaskAnchor 跨会话熔断防绕过锚点的登记与查询语义：
// 同一主会话下同任务文本命中同一锚点；不同主会话/不同任务互不影响。
func TestFailedTaskAnchor(t *testing.T) {
	m := newSubAgentManager(&Runtime{logger: logging.DefaultLogger()})

	m.recordFailedTaskAnchor("main-1", "task A", "sub-sess-1")

	// 同主会话同任务：命中
	if got := m.lookupFailedTaskAnchor("main-1", "task A"); got != "sub-sess-1" {
		t.Fatalf("同任务重派应命中失败会话锚点，实际 %q", got)
	}
	// 同主会话不同任务：不命中
	if got := m.lookupFailedTaskAnchor("main-1", "task B"); got != "" {
		t.Fatalf("不同任务不应命中锚点，实际 %q", got)
	}
	// 不同主会话同任务：不命中
	if got := m.lookupFailedTaskAnchor("main-2", "task A"); got != "" {
		t.Fatalf("跨主会话不应命中锚点，实际 %q", got)
	}

	// 同任务再次失败于另一会话：覆盖（最新失败会话为准）
	m.recordFailedTaskAnchor("main-1", "task A", "sub-sess-2")
	if got := m.lookupFailedTaskAnchor("main-1", "task A"); got != "sub-sess-2" {
		t.Fatalf("重复失败应覆盖锚点，实际 %q", got)
	}
}

// TestFailedTaskAnchorBounded 登记表上限：超限整表清空，保证内存有界。
func TestFailedTaskAnchorBounded(t *testing.T) {
	m := newSubAgentManager(&Runtime{logger: logging.DefaultLogger()})
	for i := 0; i < failedTaskAnchorLimit+10; i++ {
		m.recordFailedTaskAnchor("main-1", string(rune('a'+i%26))+string(rune('a'+i/26)), "sub")
	}
	if len(m.failedTaskAnchors) > failedTaskAnchorLimit {
		t.Fatalf("锚点登记表超限未清理：%d 条", len(m.failedTaskAnchors))
	}
}
