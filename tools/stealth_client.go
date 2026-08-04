package tools

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/textproto"
	"strings"
	"sync"
	"time"

	"github.com/DotNetAge/goharness/logging"
	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

// StealthRequest 是隐蔽客户端的统一请求结构，只用标准库类型，不外泄 fhttp 类型。
type StealthRequest struct {
	Method          string              // HTTP 方法，默认 GET
	URL             string              // 必填，完整 URL
	Headers         map[string][]string // 调用方自定义头（覆盖指纹头）
	Body            []byte              // 请求体，GET 为 nil
	FollowRedirects bool                // 是否跟随重定向，false 用于搜狗 JS 重定向解析
	Retry           *RetryPolicy        // 覆盖默认重试策略，nil 用客户端默认
}

// StealthResponse 是隐蔽客户端的统一响应结构。Body 为 io.ReadCloser，调用方负责 Close。
type StealthResponse struct {
	StatusCode int
	Headers    map[string][]string
	Body       io.ReadCloser
}

// Header 返回指定响应头的首个值（大小写不敏感）。
func (r *StealthResponse) Header(key string) string {
	if vs := r.Headers[textproto.CanonicalMIMEHeaderKey(key)]; len(vs) > 0 {
		return vs[0]
	}
	return ""
}

// RetryPolicy 描述重试 + 指数退避 + 抖动策略。
type RetryPolicy struct {
	MaxAttempts   int           // 最大尝试次数（含首次）
	BaseDelay     time.Duration // 退避基准
	MaxDelay      time.Duration // 退避上限
	JitterFactor  float64       // 抖动比例 0~1
	RetryOnStatus []int         // 触发重试的状态码
}

// defaultRetryPolicy 返回默认重试策略：最多 maxAttempts 次，500ms 基准退避，
// 4s 上限，20% 抖动，对 429/5xx 重试。
func defaultRetryPolicy(maxAttempts int) RetryPolicy {
	if maxAttempts < 1 {
		maxAttempts = 3
	}
	return RetryPolicy{
		MaxAttempts:   maxAttempts,
		BaseDelay:     500 * time.Millisecond,
		MaxDelay:      4 * time.Second,
		JitterFactor:  0.2,
		RetryOnStatus: []int{429, 500, 502, 503, 504},
	}
}

// stealthBackend 抽象 tls-client 与标准库两种后端，隔离 fhttp 类型。
type stealthBackend interface {
	do(ctx context.Context, req StealthRequest) (*StealthResponse, error)
}

// stealthClient 是统一隐蔽 HTTP 客户端，封装 TLS/HTTP2 指纹伪装、
// 浏览器指纹头注入、重试退避与预请求抖动。fetchBody 与 WebFetch 共用，
// 消除原本两套各异的请求逻辑。
//
// 后端可切换：
//   - tls-client（默认）：伪装 JA3/JA4 + HTTP/2 SETTINGS + Akamai 指纹
//   - 标准库（GOHARNESS_STEALTH_DISABLE=1）：仅 header 伪装，用于排障/兼容环境
//
// SSRF 双层防护：CheckURL 预检（调用方保留）+ 拨号层 ssrfDialContext（此处注入）。
type stealthClient struct {
	redirectBackend   stealthBackend // 跟随重定向（限 10 次 + 每次目标 SSRF 校验）
	noRedirectBackend stealthBackend // 不跟随重定向（用于搜狗 200+JS 重定向解析）
	ua                string
	profile           profiles.ClientProfile
	retry             RetryPolicy
	timeout           time.Duration
	logger            logging.Logger
	rng               *rand.Rand
	rngMu             sync.Mutex
}

// StealthOption 配置 NewStealthClient。
type StealthOption func(*stealthClientConfig)

type stealthClientConfig struct {
	timeout time.Duration
	retry   RetryPolicy
}

// WithTimeout 设置单次请求超时（不含重试退避）。
func WithTimeout(d time.Duration) StealthOption {
	return func(c *stealthClientConfig) { c.timeout = d }
}

// WithRetry 覆盖默认重试策略。
func WithRetry(r RetryPolicy) StealthOption {
	return func(c *stealthClientConfig) { c.retry = r }
}

// NewStealthClient 创建隐蔽客户端实例。
// logger 必传（nil 返回 error，遵循构造函数返回 error 偏好）；
// 读取 GOHARNESS_STEALTH_* 环境变量配置后端与策略。
// tls-client 初始化失败时自动降级到标准库后端，保证可用性。
func NewStealthClient(logger logging.Logger, opts ...StealthOption) (*stealthClient, error) {
	if logger == nil {
		return nil, errors.New("NewStealthClient: logger 不能为空")
	}
	cfg := LoadStealthConfig()
	sc := &stealthClientConfig{
		timeout: time.Duration(cfg.TimeoutSec) * time.Second,
		retry:   defaultRetryPolicy(cfg.MaxRetries),
	}
	for _, o := range opts {
		o(sc)
	}
	if sc.timeout <= 0 {
		sc.timeout = 15 * time.Second
	}

	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	pair := pickUAProfile(cfg, rng)

	c := &stealthClient{
		ua:      pair.UA,
		profile: pair.Profile,
		retry:   sc.retry,
		timeout: sc.timeout,
		logger:  logger,
		rng:     rng,
	}

	if cfg.Disabled {
		c.initStdlibBackends()
		logger.Info("[stealth]已禁用 tls-client，使用标准库后端")
	} else if err := c.initTLSBackends(); err != nil {
		// tls-client 初始化失败时降级标准库，保证可用性而非直接失败。
		logger.Warn("[stealth]tls-client 初始化失败，降级标准库后端", err)
		c.initStdlibBackends()
	}
	return c, nil
}

// initTLSBackends 用 tls-client 构造跟随/不跟随重定向两个后端，共享 fhttp cookiejar。
func (c *stealthClient) initTLSBackends() error {
	tlsLog := tls_client.NewNoopLogger()
	timeoutSec := int(c.timeout / time.Second)
	jar := tls_client.NewCookieJar()

	// 跟随重定向后端：限 10 次 + 每次目标 SSRF 校验（复用 validateURL）。
	redirectClient, err := tls_client.NewHttpClient(tlsLog,
		tls_client.WithClientProfile(c.profile),
		tls_client.WithCookieJar(jar),
		tls_client.WithDialContext(ssrfDialContext),
		tls_client.WithTimeoutSeconds(timeoutSec),
		tls_client.WithCustomRedirectFunc(func(req *fhttp.Request, via []*fhttp.Request) error {
			if len(via) >= 10 {
				return errors.New("重定向次数过多")
			}
			return validateURL(req.URL.String())
		}),
	)
	if err != nil {
		return fmt.Errorf("创建 tls-client 跟随重定向后端失败：%w", err)
	}

	// 不跟随重定向后端：用于搜狗 200+JS 重定向解析（window.location.replace）。
	noRedirectClient, err := tls_client.NewHttpClient(tlsLog,
		tls_client.WithClientProfile(c.profile),
		tls_client.WithCookieJar(jar),
		tls_client.WithDialContext(ssrfDialContext),
		tls_client.WithTimeoutSeconds(timeoutSec),
		tls_client.WithNotFollowRedirects(),
	)
	if err != nil {
		return fmt.Errorf("创建 tls-client 不跟随重定向后端失败：%w", err)
	}

	c.redirectBackend = &tlsBackend{client: redirectClient}
	c.noRedirectBackend = &tlsBackend{client: noRedirectClient}
	return nil
}

// initStdlibBackends 用标准库 net/http 构造两个后端（GOHARNESS_STEALTH_DISABLE=1 或 tls 初始化失败时）。
// 同样注入 ssrfDialContext 拨号层防护，确保 SSRF 防护不因后端切换而削弱。
func (c *stealthClient) initStdlibBackends() {
	jar, _ := cookiejar.New(nil)
	transport := &http.Transport{DialContext: ssrfDialContext}
	c.redirectBackend = &stdlibBackend{client: &http.Client{
		Timeout:   c.timeout,
		Jar:       jar,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("重定向次数过多")
			}
			return validateURL(req.URL.String())
		},
	}}
	c.noRedirectBackend = &stdlibBackend{client: &http.Client{
		Timeout:   c.timeout,
		Jar:       jar,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Do 执行隐蔽请求，含指纹头注入与重试退避。FollowRedirects 控制 backend 选择。
func (c *stealthClient) Do(ctx context.Context, req StealthRequest) (*StealthResponse, error) {
	if req.FollowRedirects {
		return c.doWith(ctx, req, c.redirectBackend)
	}
	return c.doWith(ctx, req, c.noRedirectBackend)
}

// requestLogger 优先用 ctx 中系统注入到工具的 logger（ToolContext.Logger），
// 兜底用客户端构造时的 logger。运行时日志走会话注入的 logger，而非构造时固定的
// DefaultLogger/NopLogger——与项目「Logger 必须通过注入提供，禁止内部创建日志」的约束一致。
//
// stealthClient 在工具构造期创建（此时无 ctx，无法拿到会话 logger），故构造期告警
// （如 tls-client 初始化失败降级）与 tls-client debug 日志仍用 c.logger；但真正的请求
// 日志（重试退避等）在此从 ctx 取注入实例，确保落到调用方配置的日志后端。
func (c *stealthClient) requestLogger(ctx context.Context) logging.Logger {
	if tc := GetToolContext(ctx); tc != nil && tc.Logger != nil {
		return tc.Logger
	}
	return c.logger
}

// doWith 统一执行逻辑：指纹头注入 + 重试退避。
// 每次重试从 req.Body ([]byte) 重新构造 reader，故带 body 的请求也可安全重试。
func (c *stealthClient) doWith(ctx context.Context, req StealthRequest, backend stealthBackend) (*StealthResponse, error) {
	c.applyFingerprintHeaders(&req)
	logger := c.requestLogger(ctx)
	policy := c.retry
	if req.Retry != nil {
		policy = *req.Retry
	}

	var lastResp *StealthResponse
	var lastErr error
	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		if ctx.Err() != nil {
			if lastResp != nil {
				lastResp.Body.Close()
			}
			return nil, ctx.Err()
		}
		if attempt > 0 {
			delay := c.backoff(attempt-1, policy)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
		}
		resp, err := backend.do(ctx, req)
		if !shouldRetry(resp, err, policy) || attempt == policy.MaxAttempts-1 {
			return resp, err
		}
		lastResp, lastErr = resp, err
		if resp != nil {
			resp.Body.Close()
		}
		logger.Warn("[stealth]请求将重试",
			"attempt", attempt+1,
			"url", req.URL,
		)
	}
	return lastResp, lastErr
}

// applyFingerprintHeaders 注入浏览器指纹头（Sec-Ch-Ua / Sec-Fetch-* / Platform 等）。
// UA 与 ClientProfile 配对一致；仅设置调用方未显式设置的 header，不覆盖 extraHeaders（如微信 Referer）。
// Chrome 系 UA 补全客户端提示头；Safari/Firefox 真实环境不发这些头，保持一致。
func (c *stealthClient) applyFingerprintHeaders(req *StealthRequest) {
	if req.Headers == nil {
		req.Headers = map[string][]string{}
	}
	set := func(k, v string) {
		if _, ok := req.Headers[k]; !ok {
			req.Headers[k] = []string{v}
		}
	}
	set("User-Agent", c.ua)
	set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	set("Sec-Fetch-Dest", "document")
	set("Sec-Fetch-Mode", "navigate")
	set("Sec-Fetch-Site", "none")
	set("Sec-Fetch-User", "?1")
	set("Upgrade-Insecure-Requests", "1")

	if strings.Contains(c.ua, "Chrome") {
		set("Sec-Ch-Ua", `"Chromium";v="146", "Not_A Brand";v="24", "Google Chrome";v="146"`)
		set("Sec-Ch-Ua-Mobile", "?0")
		switch {
		case strings.Contains(c.ua, "Macintosh"):
			set("Sec-Ch-Ua-Platform", `"macOS"`)
		case strings.Contains(c.ua, "Windows"):
			set("Sec-Ch-Ua-Platform", `"Windows"`)
		case strings.Contains(c.ua, "X11"), strings.Contains(c.ua, "Linux"):
			set("Sec-Ch-Ua-Platform", `"Linux"`)
		}
	}
}

// shouldRetry 判断是否重试：网络错误（非上下文取消、非确定性 SSRF/重定向超限）或命中 RetryOnStatus。
// 注意：跟随重定向后端拿到的是最终响应，搜狗 302 限速会跟随到反爬页（200），无法在此检测，
// 故 302 限速不在此重试——由多引擎回退机制（searchAllAdapters 8s deadline）兜底。
func shouldRetry(resp *StealthResponse, err error, policy RetryPolicy) bool {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return false
		}
		// 确定性错误不重试，避免无谓退避。覆盖 ssrfDialContext 与 validateURL 两种文本。
		msg := err.Error()
		if strings.Contains(msg, "私有") || strings.Contains(msg, "重定向次数过多") {
			return false
		}
		return true
	}
	if resp == nil {
		return true
	}
	for _, code := range policy.RetryOnStatus {
		if resp.StatusCode == code {
			return true
		}
	}
	return false
}

// backoff 计算第 attempt 次退避时长（含抖动），attempt 从 0 开始。
// 公式：min(MaxDelay, BaseDelay * 2^attempt) * (1 ± JitterFactor)。
func (c *stealthClient) backoff(attempt int, policy RetryPolicy) time.Duration {
	c.rngMu.Lock()
	jitter := c.rng.Float64()
	c.rngMu.Unlock()

	delay := float64(policy.BaseDelay) * float64(int64(1)<<uint(attempt))
	if delay > float64(policy.MaxDelay) {
		delay = float64(policy.MaxDelay)
	}
	spread := 1.0 + (jitter*2-1)*policy.JitterFactor
	return time.Duration(delay * spread)
}

// ssrfDialContext 是带 SSRF 防护的拨号函数，签名与 net.Dialer.DialContext 一致，
// 直接传给 tls-client 的 WithDialContext（v1.15.0+）与标准库 Transport.DialContext。
// 复用 isPrivateIP，在 socket 级拦截私有/内部地址，防 DNS rebinding（比 CheckURL 预检更紧）。
func ssrfDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil, fmt.Errorf("解析主机 %q 失败：%w", host, err)
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return nil, fmt.Errorf("访问被拒绝：URL 解析为私有/内部地址 %s", ip)
		}
	}
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return dialer.DialContext(ctx, network, addr)
}

// --- tls-client 后端 ---

// tlsBackend 用 bogdanfinn/tls-client 实现 stealthBackend，隔离 fhttp 类型。
type tlsBackend struct {
	client tls_client.HttpClient
}

func (b *tlsBackend) do(ctx context.Context, req StealthRequest) (*StealthResponse, error) {
	method := req.Method
	if method == "" {
		method = "GET"
	}
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	fReq, err := fhttp.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败：%w", err)
	}
	for k, vs := range req.Headers {
		for _, v := range vs {
			fReq.Header.Add(k, v)
		}
	}
	resp, err := b.client.Do(fReq)
	if err != nil {
		return nil, err
	}
	return &StealthResponse{
		StatusCode: resp.StatusCode,
		Headers:    map[string][]string(resp.Header),
		Body:       resp.Body,
	}, nil
}

// --- 标准库后端 ---

// stdlibBackend 用 net/http 实现 stealthBackend（GOHARNESS_STEALTH_DISABLE=1 时启用）。
type stdlibBackend struct {
	client *http.Client
}

func (b *stdlibBackend) do(ctx context.Context, req StealthRequest) (*StealthResponse, error) {
	method := req.Method
	if method == "" {
		method = "GET"
	}
	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	sReq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败：%w", err)
	}
	for k, vs := range req.Headers {
		for _, v := range vs {
			sReq.Header.Add(k, v)
		}
	}
	resp, err := b.client.Do(sReq)
	if err != nil {
		return nil, err
	}
	return &StealthResponse{
		StatusCode: resp.StatusCode,
		Headers:    map[string][]string(resp.Header),
		Body:       resp.Body,
	}, nil
}
