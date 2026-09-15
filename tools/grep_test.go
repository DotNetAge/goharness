// Grep 工具参数与行为的单元测试。
// 覆盖：rg 参数拼装、smart-case 语义、上下文行输出、排除规则、默认跳过目录、
// head_limit/offset 分页、path 边界校验、files_with_matches/count 模式。
// native 路径直接调用 runNative（不依赖沙箱注入）；rg 路径只测纯函数 buildRgArgs，
// 避免 CI 环境缺少 rg 二进制导致测试不稳定。
package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeGrepTestTree 构造固定内容的临时项目树，返回根目录。
func writeGrepTestTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		// 行1: package main / 行2: 空 / 行3: func main() { / 行4: Error A / 行5: plain / 行6: }
		"main.go":             "package main\n\nfunc main() {\n\tlog.Println(\"Error A\")\n\tfmt.Println(\"plain\")\n}\n",
		"app.ts":              "export const x = 1; // Error B\nexport const y = 2;\n",
		"notes.md":            "error lowercase C\nplain text\n",
		"node_modules/i.d.js": "Error D in deps\n",
		"testdata/skip.txt":   "Error E in testdata\n",
	}
	for rel, content := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// grepNativeResult 执行 runNative 并返回输出字符串与 hit_count。
func grepNativeResult(t *testing.T, tool *GrepTool, p grepParams, searchDir string) (string, int) {
	t.Helper()
	res, err := tool.runNative(context.Background(), p, searchDir)
	if err != nil {
		t.Fatalf("runNative(%+v): %v", p, err)
	}
	ms, ok := res.(MetaString)
	if !ok {
		t.Fatalf("runNative 返回类型异常: %T", res)
	}
	hit, _ := ms.Meta["hit_count"].(int)
	return ms.Value, hit
}

// TestGrepSmartCaseNative 验证 smart-case：小写模式忽略大小写，含大写模式区分大小写。
func TestGrepSmartCaseNative(t *testing.T) {
	root := writeGrepTestTree(t)
	tool := &GrepTool{MaxResults: 100, MaxOutputChars: 50000}

	// 全小写 pattern：忽略大小写，三个业务文件全部命中
	out, _ := grepNativeResult(t, tool, grepParams{pattern: "error", outputMode: "files_with_matches"}, root)
	for _, want := range []string{"main.go", "app.ts", "notes.md"} {
		if !strings.Contains(out, want) {
			t.Errorf("smart-case 小写模式应命中 %s，输出：%s", want, out)
		}
	}

	// 含大写 pattern：区分大小写，notes.md（纯小写）不命中
	out, _ = grepNativeResult(t, tool, grepParams{pattern: "Error", outputMode: "files_with_matches"}, root)
	if !strings.Contains(out, "main.go") || !strings.Contains(out, "app.ts") {
		t.Errorf("大写模式应命中 main.go 与 app.ts，输出：%s", out)
	}
	if strings.Contains(out, "notes.md") {
		t.Errorf("大写模式不应命中 notes.md（纯小写文本），输出：%s", out)
	}

	// ignore_case=true：强制忽略大小写
	out, _ = grepNativeResult(t, tool, grepParams{pattern: "Error", ignoreCase: true, outputMode: "files_with_matches"}, root)
	if !strings.Contains(out, "notes.md") {
		t.Errorf("ignore_case=true 应命中 notes.md，输出：%s", out)
	}
}

// TestGrepContextNative 验证上下文行输出格式与组间分隔。
func TestGrepContextNative(t *testing.T) {
	root := writeGrepTestTree(t)
	tool := &GrepTool{MaxResults: 100, MaxOutputChars: 50000}

	out, _ := grepNativeResult(t, tool, grepParams{
		pattern: "Error A", outputMode: "content", contextBefore: 2, contextAfter: 1,
	}, root)

	// 匹配行 ":"、上下文行 "-"、组间 "--"
	want := strings.Join([]string{
		"main.go-2:",
		"main.go-3:func main() {",
		"main.go:4:\tlog.Println(\"Error A\")",
		"main.go-5:\tfmt.Println(\"plain\")",
	}, "\n")
	if !strings.Contains(out, want) {
		t.Errorf("上下文输出不符。\n期望包含：\n%s\n实际：\n%s", want, out)
	}

	// context=N 等价于 before=after=N（经 validateGrepParams 归一化）
	p := validateGrepParams(map[string]any{"pattern": "x", "output_mode": "content", "context": float64(3)})
	if p.contextBefore != 3 || p.contextAfter != 3 {
		t.Errorf("context=3 应拆分为 before=3 after=3，实际 %d/%d", p.contextBefore, p.contextAfter)
	}
}

// TestGrepExcludeNative 验证 exclude 排除文件与目录。
func TestGrepExcludeNative(t *testing.T) {
	root := writeGrepTestTree(t)
	tool := &GrepTool{MaxResults: 100, MaxOutputChars: 50000}

	out, _ := grepNativeResult(t, tool, grepParams{
		pattern: "Error", outputMode: "files_with_matches", exclude: "*.ts",
	}, root)
	if strings.Contains(out, "app.ts") {
		t.Errorf("exclude=*.ts 不应命中 app.ts，输出：%s", out)
	}

	out, _ = grepNativeResult(t, tool, grepParams{
		pattern: "Error", outputMode: "files_with_matches", exclude: "testdata",
	}, root)
	if strings.Contains(out, "testdata") {
		t.Errorf("exclude=testdata 不应命中 testdata 目录，输出：%s", out)
	}
}

// TestGrepDefaultSkipDirsNative 验证默认跳过 node_modules 等重目录与隐藏文件。
func TestGrepDefaultSkipDirsNative(t *testing.T) {
	root := writeGrepTestTree(t)
	tool := &GrepTool{MaxResults: 100, MaxOutputChars: 50000}

	out, _ := grepNativeResult(t, tool, grepParams{pattern: "Error", outputMode: "files_with_matches"}, root)
	if strings.Contains(out, "node_modules") {
		t.Errorf("默认应跳过 node_modules，输出：%s", out)
	}
	if !strings.Contains(out, "testdata") {
		t.Errorf("默认不应跳过普通目录 testdata，输出：%s", out)
	}
}

// TestGrepPagingNative 验证 head_limit 与 offset 分页语义。
func TestGrepPagingNative(t *testing.T) {
	root := writeGrepTestTree(t)
	tool := &GrepTool{MaxResults: 100, MaxOutputChars: 50000}

	// notes.md + app.ts + main.go + testdata/skip.txt 共 4 条 files_with_matches 结果
	all, hit := grepNativeResult(t, tool, grepParams{pattern: "error", ignoreCase: true, outputMode: "files_with_matches"}, root)
	if hit != 4 {
		t.Fatalf("预期 4 个文件命中，实际 %d，输出：%s", hit, all)
	}

	// head_limit=2：只返回前 2 行，hit_count 仍为总数
	out, hit := grepNativeResult(t, tool, grepParams{
		pattern: "error", ignoreCase: true, outputMode: "files_with_matches", headLimit: 2,
	}, root)
	if got := len(nonEmptyLines(out)); got != 2 {
		t.Errorf("head_limit=2 应返回 2 行，实际 %d，输出：%s", got, out)
	}
	if hit != 4 {
		t.Errorf("head_limit 截断后 hit_count 应保持总数 4，实际 %d", hit)
	}

	// offset=1：跳过第一行
	out, _ = grepNativeResult(t, tool, grepParams{
		pattern: "error", ignoreCase: true, outputMode: "files_with_matches", offset: 1, headLimit: 2,
	}, root)
	lines := nonEmptyLines(out)
	fullLines := nonEmptyLines(all)
	if len(lines) != 2 {
		t.Fatalf("offset=1 head_limit=2 应返回 2 行，实际：%s", out)
	}
	if lines[0] == fullLines[0] {
		t.Errorf("offset 应跳过第一行结果，输出：%s", out)
	}
}

// TestGrepModesNative 验证 count 与 content 输出格式。
func TestGrepModesNative(t *testing.T) {
	root := writeGrepTestTree(t)
	tool := &GrepTool{MaxResults: 100, MaxOutputChars: 50000}

	out, _ := grepNativeResult(t, tool, grepParams{pattern: "error", ignoreCase: true, outputMode: "count"}, root)
	if !strings.Contains(out, "notes.md:1") || !strings.Contains(out, "app.ts:1") {
		t.Errorf("count 模式应输出 文件:计数，输出：%s", out)
	}

	out, _ = grepNativeResult(t, tool, grepParams{pattern: "plain"}, root)
	if !strings.Contains(out, "main.go:5:") || !strings.Contains(out, "notes.md:2:") {
		t.Errorf("content 模式应输出 文件:行号:内容，输出：%s", out)
	}
}

// TestGrepResolveSearchTarget 验证 path 参数边界校验。
func TestGrepResolveSearchTarget(t *testing.T) {
	root := writeGrepTestTree(t)
	tool := &GrepTool{}

	// 相对子目录
	got, err := tool.resolveSearchTarget("testdata", root)
	if err != nil || got != filepath.Join(root, "testdata") {
		t.Errorf("相对子目录解析错误: %v %s", err, got)
	}
	// 项目内绝对路径
	got, err = tool.resolveSearchTarget(root, root)
	if err != nil || got != root {
		t.Errorf("项目内绝对路径应放行: %v %s", err, got)
	}
	// 越界路径拒绝
	for _, bad := range []string{"..", "../../etc", "/etc"} {
		if _, err := tool.resolveSearchTarget(bad, root); err == nil {
			t.Errorf("越界路径 %q 应被拒绝", bad)
		}
	}
}

// TestBuildRgArgs 验证 rg 参数拼装映射。
func TestBuildRgArgs(t *testing.T) {
	args := buildRgArgs(grepParams{
		pattern: "Foo", outputMode: "content", contextBefore: 2, contextAfter: 2,
		include: "*.go", exclude: "*_test.go",
	}, "/proj")
	want := []string{
		"--no-heading", "--color", "never", "--smart-case",
		"--column", "--line-number", "-C", "2",
		"-g", "*.go", "-g", "!*_test.go",
		"Foo", "/proj",
	}
	if !reflect.DeepEqual(args, want) {
		t.Errorf("rg 参数不符。\n期望：%v\n实际：%v", want, args)
	}

	// ignore_case=true 用 -i 替代 --smart-case；count 模式无上下文参数
	args = buildRgArgs(grepParams{pattern: "x", outputMode: "count", ignoreCase: true}, "/proj")
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "--smart-case") || !strings.Contains(joined, "-i") {
		t.Errorf("ignore_case=true 应使用 -i：%s", joined)
	}
	if !strings.Contains(joined, "--count") {
		t.Errorf("count 模式应带 --count：%s", joined)
	}
}

// TestValidateGrepParamsModeGating 验证上下文仅在 content 模式生效。
func TestValidateGrepParamsModeGating(t *testing.T) {
	p := validateGrepParams(map[string]any{
		"pattern": "x", "output_mode": "files_with_matches", "context": float64(5),
	})
	if p.contextBefore != 0 || p.contextAfter != 0 {
		t.Errorf("非 content 模式应忽略上下文参数，实际 %d/%d", p.contextBefore, p.contextAfter)
	}

	// 默认（无 output_mode）视为 content
	p = validateGrepParams(map[string]any{"pattern": "x", "context": float64(5)})
	if p.contextBefore != 5 || p.contextAfter != 5 {
		t.Errorf("默认模式应应用上下文参数，实际 %d/%d", p.contextBefore, p.contextAfter)
	}
}

// TestGrepRgEarlyTerminate 验证 rg 路径的流式读取提前终止：
// 输出达到 MaxOutputChars 上限时 kill rg，不再扫完整仓库，内存有硬上界。
// 本机无 rg 时跳过（保持 CI 稳定，rg 行为不作为必测项）。
func TestGrepRgEarlyTerminate(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("本机未安装 rg，跳过 rg 路径测试")
	}
	root := t.TempDir()
	// 单文件 3000 行命中行：宽泛输出足以触发提前终止
	var sb strings.Builder
	for i := 0; i < 3000; i++ {
		sb.WriteString("match me please\n")
	}
	abs := filepath.Join(root, "big.txt")
	if err := os.WriteFile(abs, []byte(sb.String()), 0o644); err != nil {
		t.Fatalf("write big.txt: %v", err)
	}

	tool := &GrepTool{MaxResults: 100000, MaxOutputChars: 500}
	res, err := tool.runRg(context.Background(), grepParams{pattern: "match"}, root)
	if err != nil {
		t.Fatalf("runRg: %v", err)
	}
	ms, ok := res.(MetaString)
	if !ok {
		t.Fatalf("runRg 返回类型异常: %T", res)
	}
	if !strings.Contains(ms.Value, "已提前终止") {
		t.Errorf("输出超限应触发提前终止标记，实际输出尾部：%s", tailRunes(ms.Value, 120))
	}
	if len(ms.Value) > 2000 {
		t.Errorf("提前终止后输出应有硬上界，实际 %d 字节", len(ms.Value))
	}
	if hit, _ := ms.Meta["hit_count"].(int); hit <= 0 {
		t.Errorf("提前终止前应已有产出结果，hit_count=%v", ms.Meta["hit_count"])
	}
}

// tailRunes 返回字符串尾部最多 n 个字符（错误信息展示用）。
func tailRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}

// nonEmptyLines 拆分非空行。
func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
