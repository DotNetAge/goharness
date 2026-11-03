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
		LineCount: 100,
		MtimeMs:   1234567890,
	}
	setReadFileState(key, state)

	loaded, ok := getReadFileState(key)
	if !ok {
		t.Fatal("expected to find cached state")
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
	setReadFileState(key1, &ReadFileState{MtimeMs: 111})
	setReadFileState(key2, &ReadFileState{MtimeMs: 222})

	s1, ok := getReadFileState(key1)
	if !ok || s1.MtimeMs != 111 {
		t.Error("key1 should carry MtimeMs=111")
	}
	s2, ok := getReadFileState(key2)
	if !ok || s2.MtimeMs != 222 {
		t.Error("key2 should carry MtimeMs=222")
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

// TestRead_CursorScanEquivalence 验证字节层游标扫描与 split 语义的边界等价：
// 末尾空段（尾随换行产生的第 N+1 段）、offset 越界、翻页行号连续性。
func TestRead_CursorScanEquivalence(t *testing.T) {
	read, dir := tempReadTool(t, &FileReadingLimits{
		MaxSizeBytes:   256 * 1024,
		MaxOutputChars: 75000,
		DefaultLines:   10,
	})

	// 1) 尾随换行："a\nb\n" 按 split 语义为 3 段（含末尾空段），第 3 段输出空行
	file := filepath.Join(dir, "trailing.txt")
	os.WriteFile(file, []byte("a\nb\n"), 0644)
	resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": file, "limit": float64(10)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr := resultI.(*ReadResult)
	if rr.Data.TotalLines != 3 {
		t.Errorf("尾随换行文件应计 3 行（含末尾空段），实际 %d", rr.Data.TotalLines)
	}
	if !strings.HasSuffix(rr.Data.Content, "3\t\n") {
		t.Errorf("末尾空段应输出空行 3\\t\\n，实际内容：%q", rr.Data.Content)
	}

	// 2) offset 越界（startLine > totalLines）：0 行、无 more、不报错
	resultI, err = read.Execute(testCtx(t), map[string]any{"filePath": file, "offset": float64(99)})
	if err != nil {
		t.Fatalf("offset 越界不应报错: %v", err)
	}
	rr = resultI.(*ReadResult)
	if rr.Data.LinesRead != 0 || rr.Data.HasMore {
		t.Errorf("offset 越界应返回 0 行且无 more，实际 lines=%d hasMore=%v", rr.Data.LinesRead, rr.Data.HasMore)
	}

	// 3) 翻页行号连续：offset=2 limit=2 → 行 2、3
	resultI, err = read.Execute(testCtx(t), map[string]any{"filePath": file, "offset": float64(2), "limit": float64(2)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr = resultI.(*ReadResult)
	if !strings.Contains(rr.Data.Content, "2\tb\n") || strings.Contains(rr.Data.Content, "1\t") {
		t.Errorf("offset=2 应从行 2 开始，实际内容：%q", rr.Data.Content)
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

// TestRead_DedupCache_ShortFileDefaultLimitHits 短文件（行数 < 默认 limit）用默认参数
// 重读应命中 DedupCache：命中条件只比较 mtime，不比较实际行数。
func TestRead_DedupCache_ShortFileDefaultLimitHits(t *testing.T) {
	read, dir := tempReadTool(t, nil)
	file := filepath.Join(dir, "short_dedup.txt")
	os.WriteFile(file, []byte("line1\nline2\nline3\n"), 0644)

	for i := 0; i < 2; i++ {
		resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": file})
		if err != nil {
			t.Fatalf("read #%d: %v", i+1, err)
		}
		rr := resultI.(*ReadResult)
		if i == 1 && rr.Data.Suggestion != SuggestionContentUnchanged {
			t.Errorf("短文件默认参数第二次读应命中 DedupCache，实际 Suggestion=%q", rr.Data.Suggestion)
		}
	}
}

// TestRead_DedupCache_MtimeChangedMisses 文件 mtime 变化后重读不应命中 DedupCache。
func TestRead_DedupCache_MtimeChangedMisses(t *testing.T) {
	read, dir := tempReadTool(t, nil)
	file := filepath.Join(dir, "mtime_dedup.txt")
	os.WriteFile(file, []byte("content v1\n"), 0644)

	resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": file})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}

	// 显式推进 mtime，避免同毫秒写入导致误命中
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(file, future, future); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	resultI, err = read.Execute(testCtx(t), map[string]any{"filePath": file})
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	rr := resultI.(*ReadResult)
	if rr.Data.Suggestion == SuggestionContentUnchanged {
		t.Error("mtime 变化后不应命中 DedupCache")
	}
	if !strings.Contains(rr.Data.Content, "content v1") {
		t.Error("mtime 变化后应返回实际文件内容")
	}
}

// TestRead_CRLFLineEndings CRLF 文件的行内容不应残留 '\r'，行号与行数不受
// '\r' 剔除影响（游标必须越过 '\n'，否则每行之后会插入假空行并吞掉末尾内容）。
func TestRead_CRLFLineEndings(t *testing.T) {
	read, dir := tempReadTool(t, nil)

	file := filepath.Join(dir, "crlf.txt")
	os.WriteFile(file, []byte("first line\r\nsecond line\r\n"), 0644)
	resultI, err := read.Execute(testCtx(t), map[string]any{"filePath": file})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr := resultI.(*ReadResult)
	if want := "1\tfirst line\n2\tsecond line\n3\t\n"; rr.Data.Content != want {
		t.Errorf("CRLF 内容 = %q, want %q", rr.Data.Content, want)
	}

	file2 := filepath.Join(dir, "crlf_nonewline.txt")
	os.WriteFile(file2, []byte("a\r\nb\r\nc"), 0644)
	resultI, err = read.Execute(testCtx(t), map[string]any{"filePath": file2})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	rr = resultI.(*ReadResult)
	if want := "1\ta\n2\tb\n3\tc\n"; rr.Data.Content != want {
		t.Errorf("末行无换行符的 CRLF 内容 = %q, want %q", rr.Data.Content, want)
	}
}

// TestReadFileState_EntryCapFlush 整表清空策略：条目数超限后，先写入的旧条目被清、
// 最新条目仍可写入。
func TestReadFileState_EntryCapFlush(t *testing.T) {
	// 从当前计数连续写入 dedupMaxEntries+1 个条目，无论起始计数如何，
	// 超限 flush 必然发生：最早写入的条目被清空，最后写入的条目存活。
	firstKey := dedupCacheKey("/tmp/cap_flush_first.txt", 1, 1)
	setReadFileState(firstKey, &ReadFileState{MtimeMs: 1})
	lastKey := ""
	for i := 0; i <= dedupMaxEntries; i++ {
		lastKey = dedupCacheKey(fmt.Sprintf("/tmp/cap_flush_%d.txt", i), 1, 1)
		setReadFileState(lastKey, &ReadFileState{MtimeMs: int64(i)})
	}
	if _, ok := getReadFileState(firstKey); ok {
		t.Error("超限 flush 后最早写入的条目应被清空")
	}
	if _, ok := getReadFileState(lastKey); !ok {
		t.Error("超限 flush 后最新条目应可写入")
	}
}

// TestNegativeCache_ExpiredEntriesSwept 写入新负缓存条目时，已过期的旧条目被物理删除。
func TestNegativeCache_ExpiredEntriesSwept(t *testing.T) {
	expiredPath := "/tmp/negcache_expired.txt"
	// 直塞一个已过 TTL 的条目，模拟历史遗留
	negativeCache.Store(expiredPath, NegativeCacheEntry{
		Path:      expiredPath,
		CreatedAt: time.Now().Add(-2 * negativeCacheTTL),
	})

	freshPath := "/tmp/negcache_fresh.txt"
	setNegativeCache(freshPath)

	if _, ok := negativeCache.Load(expiredPath); ok {
		t.Error("过期条目应在下一次写入时被物理清扫")
	}
	if !checkNegativeCache(freshPath) {
		t.Error("新写入的条目应可命中")
	}
}
