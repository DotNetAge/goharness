package diffutil

import (
	"fmt"
	"strings"
	"testing"
)

// 预期值均按 computeLCS（DP + 回溯）与 GenerateDiff 的贪心行走推演得出，
// 并以测试运行结果实证。

// TestGenerateDiff_NoChange 内容完全一致时返回 nil hunk 与空字符串。
// 覆盖「带尾换行一致」与「两个空文件一致」两种情况。
func TestGenerateDiff_NoChange(t *testing.T) {
	hunks, diffStr := GenerateDiff("line1\nline2\n", "line1\nline2\n")
	if hunks != nil {
		t.Errorf("内容一致时 hunks 应为 nil，实际 %v", hunks)
	}
	if diffStr != "" {
		t.Errorf("内容一致时 diff 字符串应为空，实际 %q", diffStr)
	}

	hunks, diffStr = GenerateDiff("", "")
	if hunks != nil || diffStr != "" {
		t.Errorf("两个空文件应视为无变化，实际 hunks=%v diff=%q", hunks, diffStr)
	}
}

// TestGenerateDiff_SingleLineModify 真实场景：修改 Go 源码中的一个字符串字面量。
// 期望产生单个 hunk，位置在第 4 行（println 所在行）。
func TestGenerateDiff_SingleLineModify(t *testing.T) {
	old := "package main\n\nfunc main() {\n\tprintln(\"v1\")\n}\n"
	new := "package main\n\nfunc main() {\n\tprintln(\"v2\")\n}\n"

	hunks, diffStr := GenerateDiff(old, new)
	if len(hunks) != 1 {
		t.Fatalf("应产生 1 个 hunk，实际 %d 个: %+v", len(hunks), hunks)
	}
	want := Hunk{OldStart: 4, OldLines: 1, NewStart: 4, NewLines: 1,
		Content: "-\tprintln(\"v1\")\n+\tprintln(\"v2\")"}
	if hunks[0] != want {
		t.Errorf("hunk 不匹配:\n  实际 %+v\n  期望 %+v", hunks[0], want)
	}
	wantStr := "@@ -4,1 +4,1 @@\n-\tprintln(\"v1\")\n+\tprintln(\"v2\")\n"
	if diffStr != wantStr {
		t.Errorf("diff 字符串不匹配:\n  实际 %q\n  期望 %q", diffStr, wantStr)
	}
}

// TestGenerateDiff_PureInsertion 真实场景：import 块中插入一行。
// 纯插入的 hunk OldLines 应为 0（unified diff 惯例）。
func TestGenerateDiff_PureInsertion(t *testing.T) {
	old := "import (\n\t\"fmt\"\n)\n"
	new := "import (\n\t\"fmt\"\n\t\"os\"\n)\n"

	hunks, _ := GenerateDiff(old, new)
	if len(hunks) != 1 {
		t.Fatalf("应产生 1 个 hunk，实际 %d 个: %+v", len(hunks), hunks)
	}
	want := Hunk{OldStart: 3, OldLines: 0, NewStart: 3, NewLines: 1, Content: "+\t\"os\""}
	if hunks[0] != want {
		t.Errorf("hunk 不匹配:\n  实际 %+v\n  期望 %+v", hunks[0], want)
	}
}

// TestGenerateDiff_PureDeletion 真实场景：删除一行死代码注释。
// 纯删除的 hunk NewLines 应为 0。
func TestGenerateDiff_PureDeletion(t *testing.T) {
	old := "func setup() error {\n\treturn nil\n\t// dead code\n}\n"
	new := "func setup() error {\n\treturn nil\n}\n"

	hunks, _ := GenerateDiff(old, new)
	if len(hunks) != 1 {
		t.Fatalf("应产生 1 个 hunk，实际 %d 个: %+v", len(hunks), hunks)
	}
	want := Hunk{OldStart: 3, OldLines: 1, NewStart: 3, NewLines: 0, Content: "-\t// dead code"}
	if hunks[0] != want {
		t.Errorf("hunk 不匹配:\n  实际 %+v\n  期望 %+v", hunks[0], want)
	}
}

// TestGenerateDiff_MultipleHunks 真实场景：YAML 配置中两处不相邻的值修改，
// 期望产生两个独立 hunk。
func TestGenerateDiff_MultipleHunks(t *testing.T) {
	old := "server:\n  port: 8080\n  host: localhost\nlogging:\n  level: info\n"
	new := "server:\n  port: 9090\n  host: localhost\nlogging:\n  level: debug\n"

	hunks, diffStr := GenerateDiff(old, new)
	if len(hunks) != 2 {
		t.Fatalf("应产生 2 个 hunk，实际 %d 个: %+v", len(hunks), hunks)
	}
	want1 := Hunk{OldStart: 2, OldLines: 1, NewStart: 2, NewLines: 1,
		Content: "-  port: 8080\n+  port: 9090"}
	want2 := Hunk{OldStart: 5, OldLines: 1, NewStart: 5, NewLines: 1,
		Content: "-  level: info\n+  level: debug"}
	if hunks[0] != want1 {
		t.Errorf("hunk[0] 不匹配:\n  实际 %+v\n  期望 %+v", hunks[0], want1)
	}
	if hunks[1] != want2 {
		t.Errorf("hunk[1] 不匹配:\n  实际 %+v\n  期望 %+v", hunks[1], want2)
	}

	wantStr := "@@ -2,1 +2,1 @@\n-  port: 8080\n+  port: 9090\n" +
		"@@ -5,1 +5,1 @@\n-  level: info\n+  level: debug\n"
	if diffStr != wantStr {
		t.Errorf("diff 字符串不匹配:\n  实际 %q\n  期望 %q", diffStr, wantStr)
	}
}

// TestGenerateDiff_EmptyOldFile 真实场景：Write 工具从零创建新文件。
// 空原文件无删除行，全部为新增。
func TestGenerateDiff_EmptyOldFile(t *testing.T) {
	hunks, _ := GenerateDiff("", "hello\nworld\n")
	if len(hunks) != 1 {
		t.Fatalf("应产生 1 个 hunk，实际 %d 个: %+v", len(hunks), hunks)
	}
	if hunks[0].OldLines != 0 || hunks[0].NewLines != 2 {
		t.Errorf("新文件 hunk 行数不匹配: OldLines=%d NewLines=%d", hunks[0].OldLines, hunks[0].NewLines)
	}
	if hunks[0].Content != "+hello\n+world" {
		t.Errorf("新文件 hunk 内容不匹配: %q", hunks[0].Content)
	}
}

// TestGenerateDiff_Truncation 真实场景：批量替换 100 行文件中的 60 行块，
// 变化行数（130）超过 50 时 diff 字符串应截断为首尾各 10 行。
func TestGenerateDiff_Truncation(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 1; i <= 100; i++ {
		oldB.WriteString(fmt.Sprintf("line-%03d\n", i))
	}
	// 第 21~80 行（60 行）就地替换为 70 行新内容，其余保留
	for i := 1; i <= 20; i++ {
		newB.WriteString(fmt.Sprintf("line-%03d\n", i))
	}
	for i := 21; i <= 90; i++ {
		newB.WriteString(fmt.Sprintf("new-%03d\n", i))
	}
	for i := 81; i <= 100; i++ {
		newB.WriteString(fmt.Sprintf("line-%03d\n", i))
	}

	hunks, diffStr := GenerateDiff(oldB.String(), newB.String())
	if len(hunks) != 1 {
		t.Fatalf("应产生 1 个 hunk，实际 %d 个", len(hunks))
	}
	h := hunks[0]
	if h.OldStart != 21 || h.OldLines != 60 || h.NewStart != 21 || h.NewLines != 70 {
		t.Errorf("hunk 位置不匹配: %+v", h)
	}

	if !strings.Contains(diffStr, "@@ -21,60 +21,70 @@") {
		t.Errorf("diff 头不匹配: %q", diffStr[:min(len(diffStr), 40)])
	}
	// 首部保留前 10 行删除、尾部保留最后 10 行新增
	if !strings.Contains(diffStr, "-line-021") || !strings.Contains(diffStr, "-line-030") {
		t.Error("截断输出应保留首部前 10 行（-line-021 ~ -line-030）")
	}
	if !strings.Contains(diffStr, "+new-081") || !strings.Contains(diffStr, "+new-090") {
		t.Error("截断输出应保留尾部后 10 行（+new-081 ~ +new-090）")
	}
	if !strings.Contains(diffStr, "... (130 行变化，已截断) ...") {
		t.Error("截断输出应包含截断标记（130 行变化）")
	}
	// 截断范围外的行不应出现
	if strings.Contains(diffStr, "-line-031") || strings.Contains(diffStr, "+new-080") {
		t.Error("截断后不应出现首尾 10 行之外的内容")
	}
}

// TestSumChanges 验证跨 hunk 的 ±行数汇总（供 Write/Edit 回执统计使用）。
func TestSumChanges(t *testing.T) {
	add, del := SumChanges([]Hunk{
		{OldStart: 2, OldLines: 1, NewStart: 2, NewLines: 1},
		{OldStart: 5, OldLines: 1, NewStart: 5, NewLines: 1},
	})
	if add != 2 || del != 2 {
		t.Errorf("SumChanges = (%d, %d)，期望 (2, 2)", add, del)
	}

	add, del = SumChanges(nil)
	if add != 0 || del != 0 {
		t.Errorf("空 hunk 列表应返回 (0, 0)，实际 (%d, %d)", add, del)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
