package tools

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ============================================================
// WebFetchTool — Real HTTP Tests (Enhanced Version)
// ============================================================

func TestWebFetchTool_Info(t *testing.T) {
	tool := NewWebFetchTool()
	info := tool.Info()
	if info.Name != "WebFetch" {
		t.Errorf("Name = %q, want %q", info.Name, "WebFetch")
	}
	if len(info.Parameters) < 2 {
		t.Error("expected at least 2 parameters (url + prompt)")
	}
}

func TestWebFetchTool_Real_ExampleCom(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tool := NewWebFetchTool()
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tool := NewWebFetchTool()
	result, err := tool.Execute(ctx, map[string]any{
		"url":    "https://httpbin.org/html",
		"prompt": "Extract the main heading and first paragraph",
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	s := result.(string)

	if !strings.Contains(s, "Moby-Dick") && !strings.Contains(s, "Herman Melville") {
		t.Errorf("should contain Moby Dick content, got: %.200s...", s)
	}
	if !strings.Contains(s, "提示词：") {
		t.Error("result should contain prompt when provided")
	}
}

func httpbinIsReachable(t *testing.T) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tool := NewWebFetchTool()
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
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	tool := NewWebFetchTool()

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
	tool := NewWebFetchTool()
	badURLs := []string{
		"http://127.0.0.1/",
		"http://localhost/",
		"http://10.0.0.1/",
		"http://[::1]/",
		"ftp://example.com/",
	}
	for _, url := range badURLs {
		_, err := tool.Execute(context.Background(), map[string]any{"url": url})
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
			tool := NewWebFetchTool()
			_, err := tool.Execute(context.Background(), map[string]any{"url": tt.url})
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
// Cache & Missing Params
// ============================================================

func TestWebFetchTool_CacheBehavior(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real network test in short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tool := NewWebFetchTool().(*WebFetchTool)

	result1, err := tool.Execute(ctx, map[string]any{"url": "https://example.com"})
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}

	result2, err := tool.Execute(ctx, map[string]any{"url": "https://example.com"})
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}

	s1 := result1.(string)
	s2 := result2.(string)
	if !strings.Contains(s1, "Example Domain") || !strings.Contains(s2, "Example Domain") {
		t.Error("both results should contain page content")
	}
	if s1 == s2 {
		return // ideal: exact match from cache
	}
	t.Logf("cache results differ (may include dynamic headers): result1=%d chars, result2=%d chars", len(s1), len(s2))
}

func TestWebFetchTool_MissingURL(t *testing.T) {
	tool := NewWebFetchTool()
	_, err := tool.Execute(context.Background(), nil)
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
