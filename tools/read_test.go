package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ============================================================
// read_validate_test.go — 前置校验层
// ============================================================

func TestValidateReadPath_DeviceFiles(t *testing.T) {
	tests := []struct {
		path string
	}{
		{"/dev/zero"},
		{"/dev/random"},
		{"/dev/urandom"},
		{"/dev/null"},
		{"/dev/tty"},
		{"/dev/stdin"},
		{"/dev/stdout"},
		{"/dev/stderr"},
		{"/dev/fd/0"},
		{"/dev/fd/1"},
		{"/dev/fd/2"},
		{"/proc/self/fd/0"},
		{"/proc/self/fd/1"},
		{"/proc/self/fd/2"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			err := validateReadPath(tt.path)
			if err == nil {
				t.Errorf("validateReadPath(%q) expected error, got nil", tt.path)
			}
		})
	}
}

func TestValidateReadPath_BinaryExtensions(t *testing.T) {
	tests := []string{
		"/path/to/file.exe",
		"/path/to/file.dll",
		"/path/to/file.so",
		"/path/to/file.dylib",
		"/path/to/file.bin",
		"/path/to/file.class",
		"/path/to/file.pyc",
		"/path/to/file.zip",
		"/path/to/file.tar.gz",
		"/path/to/file.rar",
	}
	for _, path := range tests {
		t.Run(filepath.Base(path), func(t *testing.T) {
			err := validateReadPath(path)
			if err == nil {
				t.Errorf("validateReadPath(%q) expected error for binary extension", path)
			}
		})
	}
}

func TestValidateReadPath_NormalFiles(t *testing.T) {
	tests := []string{
		"/path/to/file.go",
		"/path/to/file.py",
		"/path/to/file.txt",
		"/path/to/file.json",
		"/path/to/file.yaml",
		"/path/to/file.md",
		"/path/to/file.html",
		"/path/to/file.css",
		"/path/to/file.js",
		"/path/to/file.ts",
	}
	for _, path := range tests {
		t.Run(filepath.Base(path), func(t *testing.T) {
			err := validateReadPath(path)
			if err != nil {
				t.Errorf("validateReadPath(%q) expected no error, got %v", path, err)
			}
		})
	}
}

func TestValidateReadPath_ImagePasses(t *testing.T) {
	// 图片文件不应被 validateRe 阻止（由专门的图片处理路径处理）
	tests := []string{
		"/path/to/image.png",
		"/path/to/image.jpg",
		"/path/to/image.jpeg",
		"/path/to/image.gif",
		"/path/to/image.svg",
	}
	for _, path := range tests {
		t.Run(filepath.Base(path), func(t *testing.T) {
			err := validateReadPath(path)
			if err != nil {
				t.Errorf("validateReadPath(%q) should pass for image files, got %v", path, err)
			}
		})
	}
}

func TestValidateReadPath_EmptyPath(t *testing.T) {
	err := validateReadPath("")
	if err != nil {
		t.Errorf("validateReadPath('') expected no error, got %v", err)
	}
}

// ============================================================
// read_dedup_test.go — 去重缓存层
// ============================================================

func TestDedupCacheKey(t *testing.T) {
	tests := []struct {
		filePath string
		offset   int
		limit    int
		expected string
	}{
		{"/path/file.txt", 1, 0, "/path/file.txt:1:0"},
		{"/path/file.txt", 100, 500, "/path/file.txt:100:500"},
		{"/path/file.txt", 0, 0, "/path/file.txt:0:0"},
		{"/path/file.txt", -1, 0, "/path/file.txt:-1:0"},
	}
	for _, tt := range tests {
		got := dedupCacheKey(tt.filePath, tt.offset, tt.limit)
		if got != tt.expected {
			t.Errorf("dedupCacheKey(%q, %d, %d) = %q, want %q", tt.filePath, tt.offset, tt.limit, got, tt.expected)
		}
	}
}

func TestReadFileState_StoreAndLoad(t *testing.T) {
	key := dedupCacheKey("/tmp/test.txt", 1, 0)
	state := &ReadFileState{
		FilePath:  "/tmp/test.txt",
		Offset:    1,
		LineCount: 100,
		MtimeMs:   1234567890,
	}
	setReadFileState(key, state)

	loaded, ok := getReadFileState(key)
	if !ok {
		t.Fatal("expected to find cached state")
	}
	if loaded.FilePath != "/tmp/test.txt" {
		t.Errorf("FilePath = %q, want %q", loaded.FilePath, "/tmp/test.txt")
	}
	if loaded.Offset != 1 {
		t.Errorf("Offset = %d, want 1", loaded.Offset)
	}
	if loaded.LineCount != 100 {
		t.Errorf("LineCount = %d, want 100", loaded.LineCount)
	}
	if loaded.MtimeMs != 1234567890 {
		t.Errorf("MtimeMs = %d, want 1234567890", loaded.MtimeMs)
	}
}

func TestReadFileState_NotFound(t *testing.T) {
	_, ok := getReadFileState("nonexistent:1")
	if ok {
		t.Error("expected false for non-existent key")
	}
}

func TestReadFileState_UniqueKeys(t *testing.T) {
	key1 := dedupCacheKey("/tmp/a.txt", 1, 0)
	key2 := dedupCacheKey("/tmp/b.txt", 1, 0)
	setReadFileState(key1, &ReadFileState{FilePath: "/tmp/a.txt", Offset: 1})
	setReadFileState(key2, &ReadFileState{FilePath: "/tmp/b.txt", Offset: 1})

	s1, ok := getReadFileState(key1)
	if !ok || s1.FilePath != "/tmp/a.txt" {
		t.Error("key1 should be /tmp/a.txt")
	}
	s2, ok := getReadFileState(key2)
	if !ok || s2.FilePath != "/tmp/b.txt" {
		t.Error("key2 should be /tmp/b.txt")
	}
}

func TestNegativeCache(t *testing.T) {
	// 确保缓存开始时是空的
	path := "/tmp/nonexistent_test_path_12345.txt"
	if checkNegativeCache(path) {
		t.Error("expected false before setting")
	}

	setNegativeCache(path)
	if !checkNegativeCache(path) {
		t.Error("expected true after setting")
	}

	// 不同路径不应命中
	if checkNegativeCache("/tmp/other.txt") {
		t.Error("expected false for non-cached path")
	}
}

func TestNegativeCache_ConcurrentAccess(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		i := i
		go func() {
			defer wg.Done()
			path := fmt.Sprintf("/tmp/concurrent_test_%d.txt", i%5)
			setNegativeCache(path)
			checkNegativeCache(path)
		}()
	}
	wg.Wait()
	// 验证无竞态（不崩溃即为通过）
}

func TestNegativeCache_TTL(t *testing.T) {
	path := "/tmp/ttl_test_path.txt"
	setNegativeCache(path)

	if !checkNegativeCache(path) {
		t.Error("expected true immediately after set")
	}

	// 验证 TTL 值为 5 分钟
	if negativeCacheTTL != 5*time.Minute {
		t.Errorf("expected TTL=5m, got %v", negativeCacheTTL)
	}
}

// ============================================================
// read_image_test.go — 图片读取
// ============================================================

func TestIsImageFile(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/path/image.png", true},
		{"/path/image.jpg", true},
		{"/path/image.jpeg", true},
		{"/path/image.gif", true},
		{"/path/image.bmp", true},
		{"/path/image.webp", true},
		{"/path/image.PNG", true}, // 大小写不敏感
		{"/path/image.JPG", true},
		{"/path/image.svg", false}, // SVG 通过 isSVGFile 检查
		{"/path/file.go", false},
		{"/path/file.txt", false},
	}
	for _, tt := range tests {
		got := isImageFile(tt.path)
		if got != tt.expected {
			t.Errorf("isImageFile(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestIsSVGFile(t *testing.T) {
	tests := []struct {
		path     string
		expected bool
	}{
		{"/path/image.svg", true},
		{"/path/image.SVG", true},
		{"/path/image.png", false},
		{"/path/image.Svg", true},
	}
	for _, tt := range tests {
		got := isSVGFile(tt.path)
		if got != tt.expected {
			t.Errorf("isSVGFile(%q) = %v, want %v", tt.path, got, tt.expected)
		}
	}
}

func TestBase64Encode(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", ""},
		{"f", "Zg=="},
		{"fo", "Zm8="},
		{"foo", "Zm9v"},
		{"foob", "Zm9vYg=="},
		{"fooba", "Zm9vYmE="},
		{"foobar", "Zm9vYmFy"},
	}
	for _, tt := range tests {
		got := base64Encode([]byte(tt.input))
		if got != tt.expected {
			t.Errorf("base64Encode(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestDefaultQuality(t *testing.T) {
	tests := []struct {
		size     int64
		expected int
	}{
		{500 * 1024, 90},       // < 1MB → 90
		{2 * 1024 * 1024, 85},  // 1-5MB → 85
		{10 * 1024 * 1024, 70}, // > 5MB → 70
	}
	for _, tt := range tests {
		got := defaultQuality(tt.size)
		if got != tt.expected {
			t.Errorf("defaultQuality(%d) = %d, want %d", tt.size, got, tt.expected)
		}
	}
}

// ============================================================
// read_limits_test.go — 配置优先级链
// ============================================================

func TestDynamicDefaultLines(t *testing.T) {
	tests := []struct {
		totalLines int
		maxLines   int
		expected   int
		name       string
	}{
		{totalLines: 100, maxLines: 500, expected: 55, name: "小文件 (<1000行)"},
		{totalLines: 5000, maxLines: 500, expected: 300, name: "中等文件 (~5000行)"},
		{totalLines: 10000, maxLines: 500, expected: 500, name: "大文件 (>9000行), 上限 500"},
		{totalLines: 20000, maxLines: 500, expected: 500, name: "超大型文件, 上限 500"},
		{totalLines: 50, maxLines: 100, expected: 52, name: "最小场景"},
		{totalLines: 0, maxLines: 500, expected: 50, name: "空文件"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dynamicDefaultLines(tt.totalLines, tt.maxLines)
			if got != tt.expected {
				t.Errorf("dynamicDefaultLines(%d, %d) = %d, want %d", tt.totalLines, tt.maxLines, got, tt.expected)
			}
		})
	}
}

// ============================================================
// read_suggestion_test.go — Suggestion 常量
// ============================================================

func TestSuggestionConstants(t *testing.T) {
	// 验证常量不为空
	constants := map[string]string{
		"SuggestionReadComplete":     SuggestionReadComplete,
		"SuggestionHasMoreLines":     SuggestionHasMoreLines,
		"SuggestionFileTooLarge":     SuggestionFileTooLarge,
		"SuggestionContentUnchanged": SuggestionContentUnchanged,
		"SuggestionDocConverted":     SuggestionDocConverted,
		"SuggestionImageRead":        SuggestionImageRead,
		"SuggestionImageFailed":      SuggestionImageFailed,
		"SuggestionEmptyFile":        SuggestionEmptyFile,
		"SuggestionPermissionDenied": SuggestionPermissionDenied,
		"SuggestionIsDirectory":      SuggestionIsDirectory,
		"SuggestionFileNotFound":     SuggestionFileNotFound,
	}
	for name, val := range constants {
		if val == "" {
			t.Errorf("constant %s should not be empty", name)
		}
	}
	// 验证常量各不相同
	seen := make(map[string]string)
	for name, val := range constants {
		if existing, ok := seen[val]; ok {
			t.Errorf("duplicate value %q between %s and %s", val, existing, name)
		}
		seen[val] = name
	}
}

// ============================================================
// content_types_test.go — ReadResult + ImageContent
// ============================================================

func TestReadResult_ImplementsAny(t *testing.T) {
	// *ReadResult 必须满足 any(interface{}), 即任何 Go 类型
	rr := &ReadResult{
		Data: &ReadData{Content: "test"},
	}
	var i any = rr
	if _, ok := i.(*ReadResult); !ok {
		t.Fatal("*ReadResult should satisfy any interface")
	}
}

func TestImageContent_Fields(t *testing.T) {
	ic := ImageContent{
		MediaType:      "image/jpeg",
		Base64Data:     base64Encode([]byte("test")),
		Width:          100,
		Height:         200,
		RawSize:        1000,
		CompressedSize: 500,
	}
	if ic.MediaType != "image/jpeg" {
		t.Errorf("MediaType = %q, want image/jpeg", ic.MediaType)
	}
	if ic.Width != 100 || ic.Height != 200 {
		t.Errorf("width/height = %dx%d, want 100x200", ic.Width, ic.Height)
	}
}

// ============================================================
// Read 集成测试 — 验证重构后的完整流程
// ============================================================

// tempReadTool 创建一个 Read 工具，其白名单包含一个临时目录。
// 临时目录的 symlink 被解析为真实路径，避免 macOS /private/var 链路的安全校验拦截。
func tempReadTool(t *testing.T, limits *FileReadingLimits) (*Read, string) {
	t.Helper()
	tempDir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(tempDir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", tempDir, err)
	}
	var read *Read
	if limits != nil {
		read = NewReadToolWithLimits(*limits)
	} else {
		read = NewReadTool()
	}
	read.AddWhiteList(realDir + string(filepath.Separator))
	return read, realDir
}

func TestRead_EmptyFile(t *testing.T) {
	read, dir := tempReadTool(t, nil)
	emptyFile := filepath.Join(dir, "empty.txt")
	os.WriteFile(emptyFile, []byte(""), 0644)

	resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": emptyFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr := resultI.(*ReadResult)
	if rr.Data.Suggestion != SuggestionEmptyFile {
		t.Errorf("expected Suggestion=%q, got %q", SuggestionEmptyFile, rr.Data.Suggestion)
	}
}

func TestRead_NegativeCache(t *testing.T) {
	read, dir := tempReadTool(t, nil)
	nonexistent := filepath.Join(dir, "nonexistent_test_path_12345.txt")

	// 第一次应触发 ENOENT 渐进兜底
	resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": nonexistent})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr := resultI.(*ReadResult)
	if rr.Data.Suggestion != SuggestionFileNotFound {
		t.Errorf("first call: expected Suggestion=%q, got %q", SuggestionFileNotFound, rr.Data.Suggestion)
	}
	// 应被写入 NegativeCache
	if !checkNegativeCache(nonexistent) {
		t.Error("expected path to be in negative cache")
	}

	// 第二次应被 NegativeCache 拦截
	resultI2, err := read.Execute(testCtx(t), map[string]any{"filePath": nonexistent})
	if err != nil {
		t.Fatalf("second call unexpected error: %v", err)
	}
	rr2 := resultI2.(*ReadResult)
	if rr2.Data.Suggestion != SuggestionFileNotFound {
		t.Errorf("second call: expected Suggestion=%q, got %q", SuggestionFileNotFound, rr2.Data.Suggestion)
	}
	if !strings.Contains(rr2.Data.Note, "此前已确认") {
		t.Errorf("second call: expected '此前已确认' in note, got %q", rr2.Data.Note)
	}
}

func TestRead_HasMoreLines(t *testing.T) {
	read, dir := tempReadTool(t, &FileReadingLimits{
		MaxSizeBytes:   256 * 1024,
		MaxOutputChars: 75000,
		DefaultLines:   10,
	})
	file := filepath.Join(dir, "multiline.txt")
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d content", i+1)
	}
	os.WriteFile(file, []byte(strings.Join(lines, "\n")), 0644)

	resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": file})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr := resultI.(*ReadResult)
	if rr.Data.Suggestion != SuggestionHasMoreLines {
		t.Errorf("expected Suggestion=%q, got %q", SuggestionHasMoreLines, rr.Data.Suggestion)
	}
	if rr.Data.HasMore != true {
		t.Error("expected has_more to be true")
	}
}

func TestRead_DedupCache(t *testing.T) {
	read, dir := tempReadTool(t, nil)
	file := filepath.Join(dir, "dedup_test.txt")
	content := "hello world\nsecond line\nthird line\n"
	os.WriteFile(file, []byte(content), 0644)

	// 第一次读
	resultI1, err := read.Execute(testCtx(t), map[string]any{
		"filePath": file,
		"offset":   float64(1),
		"limit":    float64(2),
	})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// 第二次读相同内容应命中 DedupCache → content_unchanged
	resultI2, err := read.Execute(testCtx(t), map[string]any{
		"filePath": file,
		"offset":   float64(1),
		"limit":    float64(2),
	})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}

	rr2 := resultI2.(*ReadResult)
	if rr2.Data.Suggestion != SuggestionContentUnchanged {
		t.Errorf("expected DedupCache hit: Suggestion=%q, got %q", SuggestionContentUnchanged, rr2.Data.Suggestion)
	}
	_ = resultI1
}

func TestRead_SuggestionReadComplete(t *testing.T) {
	read, dir := tempReadTool(t, nil)
	file := filepath.Join(dir, "small.txt")
	os.WriteFile(file, []byte("only one line"), 0644)

	resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": file})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr := resultI.(*ReadResult)
	if rr.Data.Suggestion != SuggestionReadComplete {
		t.Errorf("expected Suggestion=%q, got %q", SuggestionReadComplete, rr.Data.Suggestion)
	}
}

func TestRead_OverBudget_ReturnsError(t *testing.T) {
	read, dir := tempReadTool(t, &FileReadingLimits{
		MaxSizeBytes:   10 * 1024 * 1024, // 10MB
		MaxOutputChars: 300,              // 极低字符预算
		DefaultLines:   5000,
	})
	file := filepath.Join(dir, "large.txt")
	// 生成足够大的内容以触发输出预算检查
	var builder strings.Builder
	for i := 0; i < 5000; i++ {
		builder.WriteString(fmt.Sprintf("this is a very long line of text that will help trigger the output budget check %d\n", i))
	}
	os.WriteFile(file, []byte(builder.String()), 0644)

	_, err := read.Execute(testCtx(t), map[string]any{"filePath": file})
	if err == nil {
		t.Fatal("超过输出预算应返回错误")
	}
	// 错误信息应引导使用 offset/limit 分页精读，并包含文件总行数
	if !strings.Contains(err.Error(), "offset") || !strings.Contains(err.Error(), "limit") {
		t.Errorf("错误信息应引导使用 offset/limit，实际=%v", err)
	}
	if !strings.Contains(err.Error(), "5000") {
		t.Errorf("错误信息应包含文件总行数，实际=%v", err)
	}
}

func TestRead_FileTooLarge(t *testing.T) {
	read, dir := tempReadTool(t, &FileReadingLimits{
		MaxSizeBytes:   100, // 最大 100 字节
		MaxOutputChars: 75000,
		DefaultLines:   500,
	})
	file := filepath.Join(dir, "big.txt")
	// 创建超过限制的文件
	os.WriteFile(file, []byte(strings.Repeat("x", 1000)), 0644)

	resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": file})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr := resultI.(*ReadResult)
	if rr.Data.Suggestion != SuggestionFileTooLarge {
		t.Errorf("expected Suggestion=%q, got %q", SuggestionFileTooLarge, rr.Data.Suggestion)
	}
}
