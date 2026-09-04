package tools

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/DotNetAge/goharness/logging"
)

// ============================================================
// WebFetchTool — Real HTTP Tests (Enhanced Version)
// ============================================================

func TestWebFetchTool_Info(t *testing.T) {
	tool := NewWebFetchTool(logging.NewNopLogger())
	info := tool.Info()
	if info.Name != "WebFetch" {
		t.Errorf("Name = %q, want %q", info.Name, "WebFetch")
	}
	// prompt 死参数已删除，仅保留 url
	if len(info.Parameters) != 1 {
		t.Errorf("Parameters 数量 = %d, want 1（仅 url）", len(info.Parameters))
	}
	if info.Parameters[0].Name != "url" {
		t.Errorf("Parameters[0].Name = %q, want %q", info.Parameters[0].Name, "url")
	}
}

func TestWebFetchTool_Real_ExampleCom(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 15*time.Second)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{
		"url": "https://example.com",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	s := result.(string)

	if !strings.Contains(s, "Example Domain") {
		t.Errorf("should contain 'Example Domain', got: %.200s...", s)
	}
	if !strings.Contains(s, "--- 网页获取：") {
		t.Error("result should contain header")
	}
	if !strings.Contains(s, "状态：") {
		t.Error("result should contain status code")
	}
}

func TestWebFetchTool_Real_HttpbinHTML(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	if !httpbinIsReachable(t) {
		t.Skip("httpbin.org is not reachable (may be rate-limited)")
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 15*time.Second)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	result, err := tool.Execute(ctx, map[string]any{
		"url": "https://httpbin.org/html",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	s := result.(string)

	if !strings.Contains(s, "Moby-Dick") && !strings.Contains(s, "Herman Melville") {
		t.Errorf("should contain Moby Dick content, got: %.200s...", s)
	}
}

func httpbinIsReachable(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(testCtx(t), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://httpbin.org/get", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func TestWebFetchTool_Real_StatusCode404(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 15*time.Second)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())
	_, err := tool.Execute(ctx, map[string]any{"url": "https://httpbin.org/status/404"})
	if err != nil {
		t.Logf("404 page returned error (acceptable): %v", err)
	} else {
		t.Log("404 page returned content without error — also acceptable")
	}
}

func TestWebFetchTool_URLNormalization(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	ctx, cancel := context.WithTimeout(testCtx(t), 15*time.Second)
	defer cancel()

	tool := NewWebFetchTool(logging.NewNopLogger())

	tests := []struct {
		name string
		url  string
	}{
		{"auto add https", "example.com"},
		{"http → https upgrade", "http://example.com"},
		{"already https", "https://example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tool.Execute(ctx, map[string]any{"url": tt.url})
			if err != nil {
				t.Fatalf("Execute(%q) error = %v", tt.url, err)
			}
			s := result.(string)
			if !strings.Contains(s, "Example Domain") {
				t.Errorf("URL normalization failed for %q: %.100s...", tt.url, s)
			}
		})
	}
}

// ============================================================
// SSRF Protection Tests
// ============================================================

func TestWebFetchTool_SSRF_Protection(t *testing.T) {
	tool := NewWebFetchTool(logging.NewNopLogger())
	badURLs := []string{
		"http://127.0.0.1/",
		"http://localhost/",
		"http://10.0.0.1/",
		"http://[::1]/",
		"ftp://example.com/",
	}
	for _, url := range badURLs {
		_, err := tool.Execute(testCtx(t), map[string]any{"url": url})
		if err == nil {
			t.Errorf("SSRF should block %q", url)
		}
	}
}

func TestWebFetchTool_SSRF_PrivateIPs_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"10.x private", "http://10.0.0.1/", true},
		{"172.16 private", "http://172.16.0.1/", true},
		{"192.168 private", "http://192.168.1.1/", true},
		{"169.254 link-local", "http://169.254.169.254/", true},
		{"ftp scheme blocked", "ftp://example.com/file", true},
		{"file scheme blocked", "file:///etc/passwd", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewWebFetchTool(logging.NewNopLogger())
			_, err := tool.Execute(testCtx(t), map[string]any{"url": tt.url})
			if (err != nil) != tt.wantErr {
				t.Errorf("Execute() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// ============================================================
// isPrivateIP & parseCIDR — Comprehensive Tests
// ============================================================

func TestIsPrivateIP_Comprehensive(t *testing.T) {
	tests := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true}, {"127.255.255.255", true}, {"127.0.0.0", true},
		{"10.0.0.0", true}, {"10.255.255.255", true}, {"10.128.0.1", true},
		{"172.16.0.0", true}, {"172.31.255.255", true}, {"172.16.5.1", true},
		{"172.15.255.255", false}, {"172.32.0.0", false},
		{"192.168.0.0", true}, {"192.168.255.255", true}, {"192.168.1.1", true},
		{"192.167.255.255", false},
		{"169.254.0.1", true}, {"169.254.255.255", true},
		{"::1", true},
		{"fd00::1", true}, {"fdff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true},
		{"fe80::1", true},
		{"8.8.8.8", false}, {"1.1.1.1", false}, {"93.184.216.34", false},
		{"0.0.0.0", true}, {"255.255.255.255", false},
		{"::", false}, {"2001:4860:4860::8888", false},
	}
	for _, tt := range tests {
		ip := parseIP(tt.ip)
		if ip == nil {
			t.Errorf("failed to parse IP %q", tt.ip)
			continue
		}
		got := isPrivateIP(ip)
		if got != tt.want {
			t.Errorf("isPrivateIP(%q) = %v, want %v", tt.ip, got, tt.want)
		}
	}
}

func TestParseCIDR_Comprehensive(t *testing.T) {
	tests := []struct {
		cidr   string
		wantOK bool
	}{
		{"127.0.0.0/8", true}, {"10.0.0.0/8", true}, {"172.16.0.0/12", true},
		{"192.168.0.0/16", true}, {"169.254.0.0/16", true},
		{"::1/128", true}, {"fc00::/7", true}, {"fe80::/10", true}, {"0.0.0.0/8", true},
		{"/8", false}, {"abc", false}, {"", false}, {"256.0.0.0/8", false},
	}
	for _, tt := range tests {
		n := parseCIDR(tt.cidr)
		gotOK := n != nil
		if gotOK != tt.wantOK {
			t.Errorf("parseCIDR(%q) = %v, want ok=%v", tt.cidr, n, tt.wantOK)
		}
	}
}

// ============================================================
// validateURL — DNS Resolution Tests
// ============================================================

func TestValidateURL_PublicDomain(t *testing.T) {
	err := validateURL("https://example.com")
	if err != nil {
		t.Errorf("example.com should be valid public domain, got: %v", err)
	}
}

func TestValidateURL_IPv4Public(t *testing.T) {
	err := validateURL("https://8.8.8.8")
	if err != nil {
		t.Errorf("8.8.8.8 should be valid public IP, got: %v", err)
	}
}

func TestValidateURL_HTTPScheme(t *testing.T) {
	err := validateURL("http://example.com")
	if err != nil {
		t.Errorf("http scheme should be allowed, got: %v", err)
	}
}

func TestValidateURL_InvalidSchemes(t *testing.T) {
	schemes := []string{"ftp", "file", "javascript", "data", "mailto"}
	for _, scheme := range schemes {
		err := validateURL(scheme + "://example.com")
		if err == nil {
			t.Errorf("%s:// scheme should be rejected", scheme)
		}
	}
}

// ============================================================
// Missing Params
// ============================================================

func TestWebFetchTool_MissingURL(t *testing.T) {
	tool := NewWebFetchTool(logging.NewNopLogger())
	_, err := tool.Execute(testCtx(t), nil)
	if err == nil {
		t.Error("expected error for missing url parameter")
	}
}

// ============================================================
// TruncateString — Edge Cases
// ============================================================

func TestTruncateString_EdgeCases(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		maxLen    int
		wantTrail bool
	}{
		{"empty string", "", 100, false},
		{"under limit", "hello", 100, false},
		{"exact limit", "12345", 5, false},
		{"over limit", "123456", 5, true},
		{"unicode over limit", "你好世界测试", 4, true},
		{"unicode under limit", "你好", 10, false},
		{"single char", "x", 1, false},
		{"zero max", "hello", 0, false},
		{"long ascii", strings.Repeat("X", 100000), 50005, true},
		{"mixed unicode/ascii", "Hello世界Hello", 8, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateString(tt.input, tt.maxLen)
			if got == "" && tt.input != "" && tt.maxLen <= 0 {
				return
			}
			if tt.wantTrail {
				if !strings.HasSuffix(got, "...") {
					t.Errorf("expected '...' suffix for %q (max=%d), got: %q", tt.input, tt.maxLen, got)
				}
			} else {
				if strings.HasSuffix(got, "...") {
					t.Errorf("unexpected '...' suffix for %q (max=%d), got: %q", tt.input, tt.maxLen, got)
				}
			}
		})
	}
}

func TestTruncateString_NegativeMaxPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for negative maxLen")
		}
	}()
	TruncateString("hello", -1)
}

func parseIP(s string) net.IP { return net.ParseIP(s) }

// ============================================================
// WebFetch 落盘阈值与纯函数测试
// 阈值 500 行由用户指定：超过此值的页面落盘到 .webfetch/，
// 由 Read 分页读取；≤此值直返。以下测试覆盖阈值常量、行数统计、
// 缓存 key、字节截断、落盘/直返文案，无需网络。
// 注：mock server（127.0.0.1）端到端测试因 ssrfDialContext 在拨号层
// 无条件拦截私有 IP（不读沙箱 NetworkAllowSubnets）暂不可行，
// 由纯函数 + format 文案测试覆盖核心逻辑。
// ============================================================

// TestWebFetchLargePageLines_Threshold 保护落盘阈值不被误改。
// 阈值 500 行：与 Read 的默认行数（read.go:68）一致，两端语义对齐。
func TestWebFetchLargePageLines_Threshold(t *testing.T) {
	if webFetchLargePageLines != 500 {
		t.Errorf("webFetchLargePageLines = %d, want 500（与 Read 默认行数一致）", webFetchLargePageLines)
	}
}

// TestCountLines 验证行数统计与 Read 的 cat -n 行号语义一致。
func TestCountLines(t *testing.T) {
	tests := []struct {
		name string
		s    string
		want int
	}{
		{"空串", "", 0},
		{"单行无换行", "hello", 1},
		{"单行带尾换行", "hello\n", 1},
		{"两行", "a\nb", 2},
		{"两行带尾换行", "a\nb\n", 2},
		{"三行", "a\nb\nc", 3},
		{"仅换行符", "\n", 1},
		{"空行计入", "a\n\nb", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countLines(tt.s); got != tt.want {
				t.Errorf("countLines(%q) = %d, want %d", tt.s, got, tt.want)
			}
		})
	}
}

// TestWebFetchFileName_Deterministic 验证 sha256 文件名的确定性与唯一性。
// 相同 URL 须命中同一文件名；不同 URL 须不同；文件名须为 64 位 hex + .md（共 67 字节），
// 不含非法字符——用 sha256 而非 URL sanitize（用户确认 sha 方案正确：URL 可能极长，
// sanitize 截断会冲突，sha256 固定长度稳定唯一）。
func TestWebFetchFileName_Deterministic(t *testing.T) {
	u := "https://example.com/a/b"
	k1 := webFetchFileName(u)
	k2 := webFetchFileName(u)
	if k1 != k2 {
		t.Errorf("相同 URL 应产生相同文件名: %q vs %q", k1, k2)
	}
	// sha256 = 64 hex 字符 + ".md" = 67 字节
	if len(k1) != 67 {
		t.Errorf("文件名长度应为 67（64 hex + .md）, got %d: %q", len(k1), k1)
	}
	if !strings.HasSuffix(k1, ".md") {
		t.Errorf("文件名应以 .md 结尾, got %q", k1)
	}
	// 不含文件系统非法字符（sha256 hex 仅含 0-9a-f）
	if strings.ContainsAny(k1, "/:?&#%= ") {
		t.Errorf("文件名不应含非法字符, got %q", k1)
	}
	// 不同 URL 不同文件名
	k3 := webFetchFileName("https://example.com/c/d")
	if k1 == k3 {
		t.Error("不同 URL 不应产生相同文件名")
	}
}

// TestWebFetchCachePath_NoSessionDir 验证 sessionDir 为空时返回空路径。
// 这是 Execute 走"SessionDir 不可用回退截断"分支的触发条件。
func TestWebFetchCachePath_NoSessionDir(t *testing.T) {
	fp, dp := webFetchCachePath("", "https://example.com")
	if fp != "" || dp != "" {
		t.Errorf("空 sessionDir 应返回空路径, got fp=%q dp=%q", fp, dp)
	}
}

// TestWebFetchCachePath_WithSessionDir 验证有 sessionDir 时返回正确的落盘路径。
func TestWebFetchCachePath_WithSessionDir(t *testing.T) {
	fp, dp := webFetchCachePath("/tmp/sess", "https://example.com")
	wantDir := filepath.Join("/tmp/sess", webFetchDirName)
	if dp != wantDir {
		t.Errorf("dirPath = %q, want %q", dp, wantDir)
	}
	if !strings.HasPrefix(fp, dp+string(filepath.Separator)) {
		t.Errorf("filePath 应在 dirPath 下: got %q, dir=%q", fp, dp)
	}
}

// TestTruncateMarkdownToBytes 验证字节截断的 rune 安全与行边界回退。
func TestTruncateMarkdownToBytes(t *testing.T) {
	t.Run("未超限不截断", func(t *testing.T) {
		s := "短内容"
		got, truncated := truncateMarkdownToBytes(s)
		if truncated || got != s {
			t.Errorf("短内容不应截断: got %q truncated=%v", got, truncated)
		}
	})
	t.Run("超限截断且rune安全", func(t *testing.T) {
		big := strings.Repeat("a", webFetchMaxFileBytes+1000)
		got, truncated := truncateMarkdownToBytes(big)
		if !truncated {
			t.Error("超限内容应截断")
		}
		if len(got) > webFetchMaxFileBytes {
			t.Errorf("截断后不应超过上限: got %d bytes", len(got))
		}
		if !utf8.ValidString(got) {
			t.Error("截断后不是有效 UTF-8")
		}
	})
	t.Run("多字节字符截断rune安全", func(t *testing.T) {
		// 中文每个 rune 3 字节，确保截断边界落在多字节字符中间时能回退到 rune 边界
		big := strings.Repeat("你", webFetchMaxFileBytes)
		got, truncated := truncateMarkdownToBytes(big)
		if !truncated {
			t.Error("超限中文内容应截断")
		}
		if !utf8.ValidString(got) {
			t.Error("中文截断后不是有效 UTF-8")
		}
	})
}

// TestFormatWebFetchPathOutput_Wording 验证大页面落盘后的提示词文案。
// 用户要求：返回「文件已下载到：<绝对路径> 请用 Read 工具来读取搜索内容」。
// 文案须简洁——分页读法由 Read 的 Prompt 说明，WebFetch 不重复（工具正交）。
func TestFormatWebFetchPathOutput_Wording(t *testing.T) {
	out := formatWebFetchPathOutput("https://example.com", "/abs/.webfetch/abc.md", 600, 1024, false)
	// 核心提示词
	if !strings.Contains(out, "文件已下载到：/abs/.webfetch/abc.md") {
		t.Errorf("应包含「文件已下载到：<绝对路径>」, got: %s", out)
	}
	if !strings.Contains(out, "请用 Read 工具读取上述路径") {
		t.Errorf("应包含「请用 Read 工具读取上述路径」, got: %s", out)
	}
	if !strings.Contains(out, "搜索内容") {
		t.Errorf("应提及「搜索内容」, got: %s", out)
	}
	// 元信息保留
	if !strings.Contains(out, "行数：600") {
		t.Errorf("应包含行数, got: %s", out)
	}
	// 旧的冗长分页建议已移除（由 Read Prompt 负责，保持工具正交）
	if strings.Contains(out, "offset=501") {
		t.Errorf("不应再包含冗长的分页建议, got: %s", out)
	}
}

// TestFormatWebFetchPathOutput_ByteTruncated 验证字节截断时的提示。
func TestFormatWebFetchPathOutput_ByteTruncated(t *testing.T) {
	out := formatWebFetchPathOutput("https://example.com", "/abs/abc.md", 10000, 240*1024, true)
	if !strings.Contains(out, "已截断存储") {
		t.Errorf("字节截断时应提示「已截断存储」, got: %s", out)
	}
	if !strings.Contains(out, "文件已下载到") {
		t.Errorf("截断场景仍应包含落盘提示, got: %s", out)
	}
}

// TestFormatWebFetchOutput_SmallPage 验证小页面（≤500 行）直返文案。
func TestFormatWebFetchOutput_SmallPage(t *testing.T) {
	out := formatWebFetchOutput("https://example.com", "正文内容", 4, false)
	if !strings.Contains(out, "正文内容") {
		t.Errorf("小页面应包含正文, got: %s", out)
	}
	if !strings.Contains(out, "状态：200") {
		t.Errorf("应包含状态, got: %s", out)
	}
	// 小页面不应触发落盘提示
	if strings.Contains(out, "文件已下载到") {
		t.Errorf("小页面不应包含落盘提示, got: %s", out)
	}
}

// TestFormatWebFetchOutput_Truncated 验证回退截断场景的文案。
func TestFormatWebFetchOutput_Truncated(t *testing.T) {
	out := formatWebFetchOutput("https://example.com", "截断后内容", 60000, true)
	if !strings.Contains(out, "内容已被截断") {
		t.Errorf("截断时应提示, got: %s", out)
	}
}
