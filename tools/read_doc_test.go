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

// writeTestDocxRaw 按给定的 document.xml 原文构造 docx 文件（可含 numbering.xml）。
func writeTestDocxRaw(t *testing.T, dir, name, docXML, numberingXML string) string {
	t.Helper()

	var b bytes.Buffer
	zw := zip.NewWriter(&b)

	f, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("创建 document.xml 失败: %v", err)
	}
	if _, err := f.Write([]byte(docXML)); err != nil {
		t.Fatalf("写入 document.xml 失败: %v", err)
	}
	if numberingXML != "" {
		nf, err := zw.Create("word/numbering.xml")
		if err != nil {
			t.Fatalf("创建 numbering.xml 失败: %v", err)
		}
		if _, err := nf.Write([]byte(numberingXML)); err != nil {
			t.Fatalf("写入 numbering.xml 失败: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b.Bytes(), 0644); err != nil {
		t.Fatalf("写入 docx 失败: %v", err)
	}
	return path
}

// TestRead_DocxConversion_ImagePlaceholder 含内嵌图片的 DOCX 转换时写入占位行，
// 并在文末汇总图片数量，不再静默丢弃。
func TestRead_DocxConversion_ImagePlaceholder(t *testing.T) {
	read, dir := tempReadTool(t, &FileReadingLimits{
		MaxSizeBytes:   10 * 1024 * 1024,
		MaxOutputChars: 75000,
		DefaultLines:   500,
	})
	docXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:r><w:t>图片前文本</w:t></w:r></w:p>
<w:p><w:r><w:drawing><a:graphic><a:pic><a:blipFill><a:blip/></a:blipFill></a:pic></a:graphic></w:drawing></w:r></w:p>
<w:p><w:r><w:t>图片后文本</w:t></w:r></w:p>
</w:body></w:document>`
	path := writeTestDocxRaw(t, dir, "image.docx", docXML, "")

	resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr := resultI.(*ReadResult)
	if strings.Count(rr.Data.Content, "[图片]") != 1 {
		t.Errorf("应恰好写入 1 处 [图片] 占位行，实际内容=%q", rr.Data.Content)
	}
	if !strings.Contains(rr.Data.Content, "本文档含 1 处内嵌图片") {
		t.Errorf("文末应包含图片计数汇总，实际内容=%q", rr.Data.Content)
	}
	if !strings.Contains(rr.Data.Content, "图片前文本") || !strings.Contains(rr.Data.Content, "图片后文本") {
		t.Error("正文文本不应受图片占位影响")
	}
}

// TestRead_DocxConversion_ListCounterByIlvl 同 numID 两个 ilvl 在 numbering 定义中
// Ind.Left 相同时，计数器应按 ilvl 独立计数，各自从 start 开始。
func TestRead_DocxConversion_ListCounterByIlvl(t *testing.T) {
	read, dir := tempReadTool(t, &FileReadingLimits{
		MaxSizeBytes:   10 * 1024 * 1024,
		MaxOutputChars: 75000,
		DefaultLines:   500,
	})
	docXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>第一层第一条</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="1"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>第二层第一条</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>第一层第二条</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="1"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>第二层第二条</w:t></w:r></w:p>
</w:body></w:document>`
	numberingXML := `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:numbering xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:abstractNum w:abstractNumId="0">
<w:lvl w:ilvl="0"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:pPr><w:ind w:left="720"/></w:pPr></w:lvl>
<w:lvl w:ilvl="1"><w:start w:val="1"/><w:numFmt w:val="decimal"/><w:pPr><w:ind w:left="720"/></w:pPr></w:lvl>
</w:abstractNum>
<w:num w:numId="1"><w:abstractNumId w:val="0"/></w:num>
</w:numbering>`
	path := writeTestDocxRaw(t, dir, "ilvl.docx", docXML, numberingXML)

	resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": path})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr := resultI.(*ReadResult)
	content := rr.Data.Content
	// 两层各自独立计数：出现两个 "1."（每层首条）与两个 "2."（每层第二条）
	if got := strings.Count(content, "1. "); got != 2 {
		t.Errorf("两层应各自从 start=1 计数，期望 2 个 %q，实际 %d 个，内容=%q", "1. ", got, content)
	}
	if got := strings.Count(content, "2. "); got != 2 {
		t.Errorf("两层第二条应各自编号 2，期望 2 个 %q，实际 %d 个，内容=%q", "2. ", got, content)
	}
}

// TestTextToMarkdown_KeepsLeadingIndent textToMarkdown 保留前导缩进、剔除行尾空白。
func TestTextToMarkdown_KeepsLeadingIndent(t *testing.T) {
	input := "  indented code\nplain text   \r\n\ttab kept\t\n"
	got := textToMarkdown(input)
	want := "  indented code\nplain text\n\ttab kept\n\n\n"
	if got != want {
		t.Errorf("textToMarkdown = %q, want %q", got, want)
	}
}
