package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"testing"
	"time"

	"github.com/DotNetAge/goharness/logging"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 本文件是隐蔽客户端（stealthClient）的单测，覆盖：
//  1. 构造函数边界（nil logger 返回 error）
//  2. UA/profile 配对一致性（pickUAProfile）
//  3. 浏览器指纹头注入（applyFingerprintHeaders）
//  4. 重试决策（shouldRetry）
//  5. 指数退避 + 抖动（backoff）
//  6. SSRF 拨号层防护（ssrfDialContext / isPrivateIP）
//  7. StealthResponse 头部查找
//  8. 真实 TLS 指纹验证（-short 跳过）：tls-client 后端 JA3 应与标准库后端不同
//
// 纯单测不依赖网络、不初始化 tls-client（用 stealthClient 字面量），保持 go test 默认快速；
// 真实网络测试用 -short 跳过，且网络不可达时自动 t.Skip 而非失败。

// 2026 主流 UA 样本（与 stealth_config.go 的配对池一致）。
const (
	stealthChromeMacUA   = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	stealthChromeWinUA   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	stealthChromeLinuxUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
	stealthFirefoxUA     = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:147.0) Gecko/20100101 Firefox/147.0"
	stealthSafariUA      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Safari/605.1.15"
)

// ============================================================
// 构造函数
// ============================================================

// TestNewStealthClient_NilLogger_ReturnsError 验证 nil logger 时返回 error
// （遵循「构造函数返回 error 而非静默 panic」的硬性约束）。
func TestNewStealthClient_NilLogger_ReturnsError(t *testing.T) {
	c, err := NewStealthClient(nil)
	require.Error(t, err, "nil logger 必须返回 error")
	assert.Nil(t, c)
	assert.Contains(t, err.Error(), "logger")
}

// TestNewStealthClient_Valid_ReturnsClient 验证合法参数构造出可用客户端。
// 用 GOHARNESS_STEALTH_DISABLE=1 切换标准库后端，避免在纯单测中初始化 tls-client（更快）。
func TestNewStealthClient_Valid_ReturnsClient(t *testing.T) {
	t.Setenv("GOHARNESS_STEALTH_DISABLE", "1")
	c, err := NewStealthClient(logging.NewNopLogger(), WithTimeout(5*time.Second))
	require.NoError(t, err)
	require.NotNil(t, c)

	assert.NotEmpty(t, c.ua, "UA 必须非空")
	assert.Contains(t, fmt.Sprintf("%+v", c.profile), "Chrome", "auto 模式 profile 应是 Chrome 系（即便标准库后端不使用，仍由 pickUAProfile 设置）")
	assert.Equal(t, 5*time.Second, c.timeout)
	assert.IsType(t, &stdlibBackend{}, c.redirectBackend, "DISABLE=1 应使用标准库后端")
	assert.IsType(t, &stdlibBackend{}, c.noRedirectBackend)
}

// TestWithRetry_OverridesDefault 验证 WithRetry 选项覆盖默认重试策略。
func TestWithRetry_OverridesDefault(t *testing.T) {
	t.Setenv("GOHARNESS_STEALTH_DISABLE", "1")
	custom := RetryPolicy{MaxAttempts: 7, BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second, JitterFactor: 0.5}
	c, err := NewStealthClient(logging.NewNopLogger(), WithRetry(custom))
	require.NoError(t, err)
	assert.Equal(t, 7, c.retry.MaxAttempts)
	assert.Equal(t, 100*time.Millisecond, c.retry.BaseDelay)
}

// ============================================================
// pickUAProfile — UA 与 TLS profile 配对一致性
// ============================================================

// TestPickUAProfile_Auto_ReturnsChrome146 验证 auto 模式从配对池返回 Chrome 146。
// 配对池统一用 Chrome 146（2026 主流稳定版），保证 UA 与 TLS 指纹一致。
func TestPickUAProfile_Auto_ReturnsChrome146(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		pair := pickUAProfile(StealthConfig{ProfileName: "auto"}, rng)
		assert.Contains(t, pair.UA, "Chrome/146", "auto 模式应统一 Chrome 146")
		// profiles.ClientProfile 含函数字段（SpecFactory），reflect.DeepEqual 对非 nil 函数恒为 false，
		// 故不能用 assert.Equal 直接比较 profile 结构体；改用 %+v 字符串化校验（同常量函数指针地址稳定）。
		assert.Equal(t, fmt.Sprintf("%+v", profiles.Chrome_146), fmt.Sprintf("%+v", pair.Profile), "profile 必须与 UA 版本配对")
	}
}

// TestPickUAProfile_EmptyName_FallsBackToAuto 验证空 profile 名回退 auto。
func TestPickUAProfile_EmptyName_FallsBackToAuto(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	pair := pickUAProfile(StealthConfig{ProfileName: ""}, rng)
	assert.NotEmpty(t, pair.UA)
	assert.Equal(t, fmt.Sprintf("%+v", profiles.Chrome_146), fmt.Sprintf("%+v", pair.Profile), "空名回退 auto 应是 Chrome 146")
}

// TestPickUAProfile_FixedProfiles_Match 验证强制 profile 名返回严格配对的 UA+profile。
// UA 版本号必须与 ClientProfile 严格对应，否则 TLS 指纹与 UA 不一致会被风控识别。
func TestPickUAProfile_FixedProfiles_Match(t *testing.T) {
	cases := []struct {
		name    string
		uaSub   string
		profile profiles.ClientProfile
	}{
		{"chrome_146", "Chrome/146", profiles.Chrome_146},
		{"chrome_144", "Chrome/144", profiles.Chrome_144},
		{"firefox_147", "Firefox/147", profiles.Firefox_147},
		{"safari_18", "Version/18.0", profiles.Safari_16_0},
	}
	rng := rand.New(rand.NewSource(3))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pair := pickUAProfile(StealthConfig{ProfileName: c.name}, rng)
			assert.Contains(t, pair.UA, c.uaSub, "UA 应包含版本子串")
			assert.Equal(t, fmt.Sprintf("%+v", c.profile), fmt.Sprintf("%+v", pair.Profile), "profile 必须严格配对")
		})
	}
}

// TestPickUAProfile_UnknownName_FallsBackToAuto 验证未知 profile 名回退 auto 池，
// 保证永不返回零值（零值 UA/profile 会导致请求裸奔）。
func TestPickUAProfile_UnknownName_FallsBackToAuto(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	pair := pickUAProfile(StealthConfig{ProfileName: "edge_999"}, rng)
	assert.NotEmpty(t, pair.UA, "未知名应回退 auto，UA 非空")
	assert.Equal(t, fmt.Sprintf("%+v", profiles.Chrome_146), fmt.Sprintf("%+v", pair.Profile), "回退 auto 应是 Chrome 146")
}

// ============================================================
// applyFingerprintHeaders — 浏览器指纹头注入
// ============================================================

// TestApplyFingerprintHeaders_DefaultHeaders 验证默认指纹头齐全。
func TestApplyFingerprintHeaders_DefaultHeaders(t *testing.T) {
	c := &stealthClient{ua: stealthChromeMacUA}
	req := &StealthRequest{}
	c.applyFingerprintHeaders(req)

	assert.Equal(t, stealthChromeMacUA, req.Headers["User-Agent"][0])
	assert.Contains(t, req.Headers["Accept"][0], "text/html")
	assert.Contains(t, req.Headers["Accept-Language"][0], "zh-CN")
	assert.Equal(t, "document", req.Headers["Sec-Fetch-Dest"][0])
	assert.Equal(t, "navigate", req.Headers["Sec-Fetch-Mode"][0])
	assert.Equal(t, "none", req.Headers["Sec-Fetch-Site"][0])
	assert.Equal(t, "?1", req.Headers["Sec-Fetch-User"][0])
	assert.Equal(t, "1", req.Headers["Upgrade-Insecure-Requests"][0])
}

// TestApplyFingerprintHeaders_Chrome_SetsClientHints 验证 Chrome UA 注入客户端提示头。
func TestApplyFingerprintHeaders_Chrome_SetsClientHints(t *testing.T) {
	cases := []struct {
		name     string
		ua       string
		platform string
	}{
		{"macOS", stealthChromeMacUA, `"macOS"`},
		{"Windows", stealthChromeWinUA, `"Windows"`},
		{"Linux", stealthChromeLinuxUA, `"Linux"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cl := &stealthClient{ua: c.ua}
			req := &StealthRequest{}
			cl.applyFingerprintHeaders(req)
			assert.Contains(t, req.Headers["Sec-Ch-Ua"][0], "Chromium", c.name)
			assert.Contains(t, req.Headers["Sec-Ch-Ua"][0], "146", c.name)
			assert.Equal(t, "?0", req.Headers["Sec-Ch-Ua-Mobile"][0], c.name)
			assert.Equal(t, c.platform, req.Headers["Sec-Ch-Ua-Platform"][0], c.name)
		})
	}
}

// TestApplyFingerprintHeaders_Firefox_NoClientHints 验证 Firefox UA 不发客户端提示头。
// 真实 Firefox 不发 Sec-Ch-Ua，注入反而是爬虫特征。
func TestApplyFingerprintHeaders_Firefox_NoClientHints(t *testing.T) {
	c := &stealthClient{ua: stealthFirefoxUA}
	req := &StealthRequest{}
	c.applyFingerprintHeaders(req)
	_, hasChUa := req.Headers["Sec-Ch-Ua"]
	assert.False(t, hasChUa, "Firefox 不应发 Sec-Ch-Ua")
	_, hasPlatform := req.Headers["Sec-Ch-Ua-Platform"]
	assert.False(t, hasPlatform, "Firefox 不应发 Sec-Ch-Ua-Platform")
}

// TestApplyFingerprintHeaders_Safari_NoClientHints 验证 Safari UA 不发客户端提示头。
func TestApplyFingerprintHeaders_Safari_NoClientHints(t *testing.T) {
	c := &stealthClient{ua: stealthSafariUA}
	req := &StealthRequest{}
	c.applyFingerprintHeaders(req)
	_, hasChUa := req.Headers["Sec-Ch-Ua"]
	assert.False(t, hasChUa, "Safari 不应发 Sec-Ch-Ua")
}

// TestApplyFingerprintHeaders_DoesNotOverrideCallerHeaders 验证调用方自定义头不被覆盖。
// 例如微信适配器的 Referer 必须保留，否则被风控。
func TestApplyFingerprintHeaders_DoesNotOverrideCallerHeaders(t *testing.T) {
	c := &stealthClient{ua: stealthChromeMacUA}
	req := &StealthRequest{
		Headers: map[string][]string{
			"User-Agent": {"MyCustom/1.0"},
			"Referer":    {"https://weixin.sogou.com/"},
		},
	}
	c.applyFingerprintHeaders(req)
	assert.Equal(t, []string{"MyCustom/1.0"}, req.Headers["User-Agent"], "调用方 UA 必须保留")
	assert.Equal(t, []string{"https://weixin.sogou.com/"}, req.Headers["Referer"], "调用方 Referer 必须保留")
	// 未被覆盖的指纹头仍应注入
	assert.Equal(t, "document", req.Headers["Sec-Fetch-Dest"][0])
}

// TestApplyFingerprintHeaders_NilHeadersMap 验证 Headers 为 nil 时自动初始化。
func TestApplyFingerprintHeaders_NilHeadersMap(t *testing.T) {
	c := &stealthClient{ua: stealthChromeMacUA}
	req := &StealthRequest{Headers: nil}
	c.applyFingerprintHeaders(req)
	require.NotNil(t, req.Headers, "nil Headers 应自动初始化")
	assert.NotEmpty(t, req.Headers["User-Agent"])
}

// ============================================================
// shouldRetry — 重试决策
// ============================================================

// TestShouldRetry_Table 表驱动验证重试决策：
// 网络错误重试；确定性错误（私有地址/重定向超限/上下文取消）不重试；命中 RetryOnStatus 重试。
func TestShouldRetry_Table(t *testing.T) {
	policy := defaultRetryPolicy(3)
	cases := []struct {
		name string
		resp *StealthResponse
		err  error
		want bool
	}{
		{"网络错误重试", nil, errors.New("dial tcp: connection refused"), true},
		{"nil响应重试", nil, nil, true},
		{"上下文取消不重试", nil, context.Canceled, false},
		{"上下文超时不重试", nil, context.DeadlineExceeded, false},
		{"私有地址错误不重试", nil, errors.New("访问被拒绝：URL 解析为私有/内部地址 127.0.0.1"), false},
		{"重定向超限不重试", nil, errors.New("重定向次数过多"), false},
		{"200不重试", &StealthResponse{StatusCode: 200}, nil, false},
		{"404不重试", &StealthResponse{StatusCode: 404}, nil, false},
		{"429重试", &StealthResponse{StatusCode: 429}, nil, true},
		{"500重试", &StealthResponse{StatusCode: 500}, nil, true},
		{"502重试", &StealthResponse{StatusCode: 502}, nil, true},
		{"503重试", &StealthResponse{StatusCode: 503}, nil, true},
		{"504重试", &StealthResponse{StatusCode: 504}, nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, shouldRetry(c.resp, c.err, policy))
		})
	}
}

// ============================================================
// backoff — 指数退避 + 抖动
// ============================================================

// TestBackoff_WithinJitterBounds 验证退避时长落在 [base*(1-J), base*(1+J)] 区间内。
func TestBackoff_WithinJitterBounds(t *testing.T) {
	policy := RetryPolicy{BaseDelay: 500 * time.Millisecond, MaxDelay: 4 * time.Second, JitterFactor: 0.2}
	c := &stealthClient{rng: rand.New(rand.NewSource(time.Now().UnixNano()))}

	for attempt := 0; attempt <= 5; attempt++ {
		base := float64(policy.BaseDelay) * float64(int64(1)<<uint(attempt))
		if base > float64(policy.MaxDelay) {
			base = float64(policy.MaxDelay)
		}
		// 加 1ns 容差抵消 time.Duration 截断
		lo := time.Duration(base*(1-policy.JitterFactor)) - time.Millisecond
		hi := time.Duration(base*(1+policy.JitterFactor)) + time.Millisecond
		for i := 0; i < 100; i++ {
			d := c.backoff(attempt, policy)
			assert.GreaterOrEqual(t, int64(d), int64(lo), "attempt=%d 第%d次低于下界", attempt, i)
			assert.LessOrEqual(t, int64(d), int64(hi), "attempt=%d 第%d次高于上界", attempt, i)
		}
	}
}

// TestBackoff_CappedAtMaxDelay 验证退避被 MaxDelay 封顶（attempt 很大时不再翻倍）。
func TestBackoff_CappedAtMaxDelay(t *testing.T) {
	policy := RetryPolicy{BaseDelay: 500 * time.Millisecond, MaxDelay: 4 * time.Second, JitterFactor: 0.2}
	c := &stealthClient{rng: rand.New(rand.NewSource(1))}

	// attempt=12 → 500*4096=2048s，应被截到 4s
	lo := time.Duration(float64(policy.MaxDelay)*(1-policy.JitterFactor)) - time.Millisecond
	hi := time.Duration(float64(policy.MaxDelay)*(1+policy.JitterFactor)) + time.Millisecond
	for i := 0; i < 50; i++ {
		d := c.backoff(12, policy)
		assert.GreaterOrEqual(t, int64(d), int64(lo), "封顶后不应低于 MaxDelay*(1-J)")
		assert.LessOrEqual(t, int64(d), int64(hi), "封顶后不应高于 MaxDelay*(1+J)")
	}
}

// TestBackoff_GrowsExponentially 验证退避随 attempt 指数增长（在封顶前）。
// attempt1 的最小值（800ms）应大于 attempt0 的最大值（600ms）。
func TestBackoff_GrowsExponentially(t *testing.T) {
	policy := RetryPolicy{BaseDelay: 500 * time.Millisecond, MaxDelay: 4 * time.Second, JitterFactor: 0.2}
	c := &stealthClient{rng: rand.New(rand.NewSource(time.Now().UnixNano()))}

	maxA0 := time.Duration(0)
	minA1 := time.Hour
	for i := 0; i < 200; i++ {
		if d := c.backoff(0, policy); d > maxA0 {
			maxA0 = d
		}
		if d := c.backoff(1, policy); d < minA1 {
			minA1 = d
		}
	}
	assert.Less(t, int64(maxA0), int64(minA1), "attempt1 退避应严格大于 attempt0（指数增长）")
}

// ============================================================
// defaultRetryPolicy
// ============================================================

// TestDefaultRetryPolicy_Defaults 验证默认重试策略参数。
func TestDefaultRetryPolicy_Defaults(t *testing.T) {
	p := defaultRetryPolicy(5)
	assert.Equal(t, 5, p.MaxAttempts)
	assert.Equal(t, 500*time.Millisecond, p.BaseDelay)
	assert.Equal(t, 4*time.Second, p.MaxDelay)
	assert.InDelta(t, 0.2, p.JitterFactor, 1e-9)
	assert.Equal(t, []int{429, 500, 502, 503, 504}, p.RetryOnStatus)

	// maxAttempts < 1 应回退到 3
	p2 := defaultRetryPolicy(0)
	assert.Equal(t, 3, p2.MaxAttempts)
	p3 := defaultRetryPolicy(-1)
	assert.Equal(t, 3, p3.MaxAttempts)
}

// ============================================================
// StealthResponse.Header
// ============================================================

// TestStealthResponse_Header_Lookup 验证响应头查找大小写不敏感、返回首个值。
func TestStealthResponse_Header_Lookup(t *testing.T) {
	resp := &StealthResponse{
		Headers: map[string][]string{
			"Content-Type": {"text/html; charset=utf-8"},
			"X-Multi":      {"a", "b"},
		},
	}
	assert.Equal(t, "text/html; charset=utf-8", resp.Header("Content-Type"))
	assert.Equal(t, "text/html; charset=utf-8", resp.Header("content-type"), "应大小写不敏感")
	assert.Equal(t, "a", resp.Header("X-Multi"), "多值应返回首个")
	assert.Empty(t, resp.Header("Missing"), "缺失头应返回空串")
}

// ============================================================
// SSRF 防护
// ============================================================

// TestIsPrivateIP_Table 表驱动验证私有/内部 IP 识别。
func TestIsPrivateIP_Table(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true},   // 回环
		{"10.1.2.3", true},    // A 类私有
		{"192.168.1.1", true}, // C 类私有
		{"172.16.0.1", true},  // B 类私有
		{"169.254.1.1", true}, // 链路本地
		{"100.64.0.1", true},  // CGNAT（含云元数据）
		{"8.8.8.8", false},    // 公网
		{"1.1.1.1", false},    // 公网
		{"172.32.0.1", false}, // 不在 172.16/12 范围
		{"::1", true},         // IPv6 回环
		{"fc00::1", true},     // IPv6 唯一本地
		{"fe80::1", true},     // IPv6 链路本地
	}
	for _, c := range cases {
		t.Run(c.ip, func(t *testing.T) {
			ip := net.ParseIP(c.ip)
			require.NotNil(t, ip, "无法解析 IP %q", c.ip)
			assert.Equal(t, c.want, isPrivateIP(ip))
		})
	}
}

// TestSSRFDialContext_PrivateIP_Rejected 验证拨号层拦截私有地址（不实际拨号，快速）。
// 这是双层 SSRF 防护的拨号层核心：即便 CheckURL 预检放行，拨号层仍拦截，防 DNS rebinding。
func TestSSRFDialContext_PrivateIP_Rejected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	for _, addr := range []string{"127.0.0.1:80", "10.0.0.1:80", "192.168.1.1:80"} {
		conn, err := ssrfDialContext(ctx, "tcp", addr)
		if conn != nil {
			conn.Close()
		}
		require.Error(t, err, "%s 应被拨号层拒绝", addr)
		assert.Contains(t, err.Error(), "私有", "%s 错误信息应含「私有」", addr)
	}
}

// ============================================================
// 真实网络测试（-short 跳过，网络不可达时 t.Skip 而非失败）
// ============================================================

// TestStealthClient_RealRequest_ExampleCom 验证隐蔽客户端能完整抓取一个公网 HTTPS 站点。
// 覆盖端到端：TLS 握手（tls-client）→ 指纹头注入 → 响应读取。
func TestStealthClient_RealRequest_ExampleCom(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过真实网络测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	c, err := NewStealthClient(logging.NewNopLogger(), WithTimeout(15*time.Second))
	require.NoError(t, err)

	resp, err := c.Do(ctx, StealthRequest{
		Method:          "GET",
		URL:             "https://example.com",
		FollowRedirects: true,
	})
	if err != nil {
		t.Skipf("网络不可达，跳过：%v", err)
	}
	defer resp.Body.Close()

	assert.Equal(t, 200, resp.StatusCode)
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	assert.Contains(t, string(body), "Example Domain", "应抓取到 example.com 内容")
}

// TestStealthClient_RealTLSFingerprint_DiffersFromStdlib 是 TLS 指纹伪装的核心验证：
// 用 tls-client 后端（Chrome 146 profile）与标准库后端分别请求 tls.peet.ws/api/all，
// 断言两者的 JA3 哈希不同——若相同，说明 TLS 指纹伪装未生效（仍是 Go 标准库指纹）。
//
// tls.peet.ws 返回 ClientHello 的 JA3/JA4 指纹；tls-client 通过 uTLS 伪装成 Chrome，
// 故 JA3 应为 Chrome 指纹，与标准库的 Go 指纹不同。
func TestStealthClient_RealTLSFingerprint_DiffersFromStdlib(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过真实网络测试")
	}

	var tlsJA3, stdlibJA3 string

	// tls-client 后端（默认，GOHARNESS_STEALTH_DISABLE=0）
	t.Run("tls_client_backend", func(t *testing.T) {
		t.Setenv("GOHARNESS_STEALTH_DISABLE", "0")
		c, err := NewStealthClient(logging.NewNopLogger(), WithTimeout(25*time.Second))
		require.NoError(t, err)
		if _, ok := c.redirectBackend.(*tlsBackend); !ok {
			t.Skip("tls-client 后端初始化失败（本机环境不支持），跳过指纹比对")
		}
		tlsJA3 = fetchJA3Hash(t, c)
	})

	// 标准库后端（GOHARNESS_STEALTH_DISABLE=1）
	t.Run("stdlib_backend", func(t *testing.T) {
		t.Setenv("GOHARNESS_STEALTH_DISABLE", "1")
		c, err := NewStealthClient(logging.NewNopLogger(), WithTimeout(25*time.Second))
		require.NoError(t, err)
		require.IsType(t, &stdlibBackend{}, c.redirectBackend, "DISABLE=1 应使用标准库后端")
		stdlibJA3 = fetchJA3Hash(t, c)
	})

	if tlsJA3 == "" || stdlibJA3 == "" {
		t.Skip("无法获取 JA3 指纹（tls.peet.ws 不可达），跳过比对")
	}
	t.Logf("tls-client JA3 hash: %s", tlsJA3)
	t.Logf("stdlib    JA3 hash: %s", stdlibJA3)
	assert.NotEqual(t, stdlibJA3, tlsJA3,
		"tls-client 后端的 JA3 应与标准库不同，否则 TLS 指纹伪装未生效")
}

// fetchJA3Hash 通过隐蔽客户端请求 tls.peet.ws/api/all，解析返回的 ja3_hash 字段。
// 任何失败均返回空串（调用方据此 t.Skip，而非让测试失败）。
func fetchJA3Hash(t *testing.T, c *stealthClient) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 用 /api/clean（扁平结构，ja3/ja3_hash 在顶层）而非 /api/all（ja3 嵌套在 tls 对象内，扁平解析会拿到空值）。
	resp, err := c.Do(ctx, StealthRequest{
		Method:          "GET",
		URL:             "https://tls.peet.ws/api/clean",
		FollowRedirects: true,
	})
	if err != nil {
		t.Logf("tls.peet.ws 请求失败：%v", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Logf("tls.peet.ws 状态码 %d", resp.StatusCode)
		return ""
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var v struct {
		JA3Hash string `json:"ja3_hash"`
		JA3     string `json:"ja3"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		t.Logf("解析 tls.peet.ws 响应失败：%v", err)
		return ""
	}
	t.Logf("JA3=%s | hash=%s", truncateForLog(v.JA3, 80), v.JA3Hash)
	return v.JA3Hash
}
