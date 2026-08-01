package tools

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestDocx 在指定目录下构造一个包含 paragraphs 个段落的 docx 文件。
func writeTestDocx(t *testing.T, dir string, paragraphs int, line string) string {
	t.Helper()

	var b bytes.Buffer
	zw := zip.NewWriter(&b)

	docXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`
	for i := 0; i < paragraphs; i++ {
		docXML += fmt.Sprintf("<w:p><w:r><w:t>%s</w:t></w:r></w:p>", line)
	}
	docXML += "</w:body></w:document>"

	f, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("创建 document.xml 失败: %v", err)
	}
	if _, err := f.Write([]byte(docXML)); err != nil {
		t.Fatalf("写入 document.xml 失败: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}

	path := filepath.Join(dir, "test.docx")
	if err := os.WriteFile(path, b.Bytes(), 0644); err != nil {
		t.Fatalf("写入 docx 失败: %v", err)
	}
	return path
}

// TestRead_DocxConversion_Normal 文档转换未超预算时正常返回完整内容，并带格式元数据。
func TestRead_DocxConversion_Normal(t *testing.T) {
	read, dir := tempReadTool(t, &FileReadingLimits{
		MaxSizeBytes:   10 * 1024 * 1024,
		MaxOutputChars: 75000,
		DefaultLines:   500,
	})
	path := writeTestDocx(t, dir, 10, "普通段落文本内容")

	resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr := resultI.(*ReadResult)
	if rr.Data.Suggestion != SuggestionReadComplete {
		t.Errorf("期望 Suggestion=%q，实际=%q", SuggestionReadComplete, rr.Data.Suggestion)
	}
	if !strings.Contains(rr.Data.Content, "普通段落文本内容") {
		t.Error("应包含转换后的文档内容")
	}
	if rr.Data.Format != "docx" {
		t.Errorf("期望 Format=docx，实际=%q", rr.Data.Format)
	}
	if !strings.Contains(rr.Data.Note, "已将 DOCX 文件转换为 Markdown 格式") {
		t.Errorf("Note 应包含格式转换说明，实际=%q", rr.Data.Note)
	}
}

// TestRead_DocxConversion_OverBudget_ReturnsError 文档转换结果超过输出字符预算时，
// 与文本路径统一返回错误并引导 offset/limit 分页精读。
func TestRead_DocxConversion_OverBudget_ReturnsError(t *testing.T) {
	read, dir := tempReadTool(t, &FileReadingLimits{
		MaxSizeBytes:   10 * 1024 * 1024,
		MaxOutputChars: 300, // 极低字符预算，必然触发检查
		DefaultLines:   500,
	})
	// 200 个段落 × 每段约 13 字符 ≈ 2600 字符 >> 300
	path := writeTestDocx(t, dir, 200, "这是一段很长的测试文本内容")

	_, err := read.Execute(testCtx(t), map[string]any{"filePath": path})
	if err == nil {
		t.Fatal("超过输出预算应返回错误")
	}
	// 错误信息应引导使用 offset/limit 分页精读
	if !strings.Contains(err.Error(), "offset") || !strings.Contains(err.Error(), "limit") {
		t.Errorf("错误信息应引导使用 offset/limit，实际=%v", err)
	}
}

// TestRead_DocxConversion_OffsetLimit 文档转换结果按纯文本处理，支持 offset/limit 分页精读。
func TestRead_DocxConversion_OffsetLimit(t *testing.T) {
	read, dir := tempReadTool(t, &FileReadingLimits{
		MaxSizeBytes:   10 * 1024 * 1024,
		MaxOutputChars: 75000,
		DefaultLines:   500,
	})
	path := writeTestDocx(t, dir, 20, "分页段落文本内容")

	resultI, err := read.Execute(testCtx(t), map[string]any{
		"filePath": path,
		"offset":   float64(5),
		"limit":    float64(3),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr := resultI.(*ReadResult)
	if rr.Data.StartLine != 5 {
		t.Errorf("期望 StartLine=5，实际=%d", rr.Data.StartLine)
	}
	if rr.Data.LinesRead != 3 {
		t.Errorf("期望 LinesRead=3，实际=%d", rr.Data.LinesRead)
	}
	if !strings.Contains(rr.Data.Content, "5\t") {
		t.Error("内容应包含第 5 行的行号前缀")
	}
}
