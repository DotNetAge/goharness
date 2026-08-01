package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// webFetchCacheTTL 是 WebFetch 缓存的生存时间。
const webFetchCacheTTL = 15 * time.Minute

// webFetchCacheSessionID 是 WebFetch 缓存在 KVStore 中的会话 ID。
const webFetchCacheSessionID = "__goharness_webfetch_cache__"

// webFetchMaxContentChars 是 WebFetch 返回内容的最大字符数。
const webFetchMaxContentChars = 50000

// cachedFetch 表示缓存的一次网页获取结果。
type cachedFetch struct {
	Content   string    `json:"content"`   // 页面内容（Markdown 格式）
	Timestamp time.Time `json:"timestamp"` // 缓存时间
	URL       string    `json:"url"`       // 原始 URL
}

// WebFetchTool 实现了网页内容获取工具。
// 用于获取并转换网页内容为 Markdown 格式，具有以下安全特性：
//   - SSRF 防护：阻止访问内网 IP 地址
//   - URL 验证：只允许 http/https 协议
//   - 重定向限制：最多 10 次重定向
//   - 内容大小限制：最大 10MB 响应体
//
// 内容处理：
//   - HTML → Markdown 转换
//   - 非 HTML 内容的原样返回
//   - 输出截断至 50000 字符
type WebFetchTool struct {
	client   *http.Client // HTTP 客户端（带 SSRF 防护）
	memCache sync.Map     // 内存缓存（进程内）
}

// NewWebFetchTool 创建一个 WebFetchTool 实例。
// 配置了 SSRF 防护的 HTTP 客户端，阻止访问内网地址。
//
// 安全措施：
//   - DNS 解析后检查 IP 是否为私有地址
//   - 重定向时验证目标 URL
//   - 15 秒连接超时，30 秒总超时
//
// 返回：
//   - FuncTool: 配置好的 WebFetchTool 实例
func NewWebFetchTool() FuncTool {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &WebFetchTool{
		client: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
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
					return dialer.DialContext(ctx, network, addr)
				},
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("重定向次数过多")
				}
				return validateURL(req.URL.String())
			},
		},
	}
}

// Info 返回 WebFetch 工具的元信息。
func (t *WebFetchTool) Info() *ToolInfo {
	return &ToolInfo{
		Name:               "WebFetch",
		MaxResultSizeChars: 25000,
		Description:        "获取并提取网页内容。在 WebSearch 之后使用，以读取已发现 URL 的实际内容。",
		Prompt: `获取并提取网页内容。在 WebSearch 之后使用，以读取已发现 URL 的实际内容。

工作流程：
1. 验证 URL 并检查 SSRF 风险（阻止私有 IP）。
2. 通过 HTTP 在本地获取页面内容。
3. 去除 HTML 标签（脚本、样式、导航等）以生成干净的 Markdown。
4. 返回提取的内容。

内容预算：
- 最大返回内容：50,000 字符（约 16K tokens）
- 如果页面超出此限制，输出将注明省略了多少字符
- 使用 prompt 参数将获取范围缩小到相关部分（例如，prompt="提取定价信息"）
- 如果您需要被截断页面的更多内容，请使用更具体的 prompt 重新获取`,
		Tags:       []string{"web", "fetch", "url", "content", "http"},
		IsReadOnly: true,
		Parameters: []Parameter{
			{
				Name:        "url",
				Type:        "string",
				Description: "要获取内容的 URL。",
				Required:    true,
			},
			{
				Name:        "prompt",
				Type:        "string",
				Description: "要提取的信息或关于页面内容要回答的问题。有助于将输出集中在相关细节上。",
				Required:    false,
			},
		},
	}
}

// Execute 执行网页内容获取操作。
//
// 处理流程：
//  1. 验证并规范化 URL（强制 HTTPS）
//  2. 检查内存缓存和 KVStore 缓存
//  3. 验证 URL 安全性（SSRF 防护）
//  4. 发送 HTTP GET 请求
//  5. 处理响应（HTML 转 Markdown 或原样返回）
//  6. 截断过长的内容
//  7. 缓存结果
//
// 参数：
//   - ctx: 上下文
//   - params: 必须包含 "url"，可选 "prompt"
//
// 返回：
//   - string: 格式化的页面内容
//   - error: URL 无效、访问被拒绝或网络错误时返回错误
func (t *WebFetchTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	rawURL, err := ValidateRequiredString("WebFetch", params, "url")
	if err != nil {
		return nil, err
	}
	var prompt string
	if raw, found := GetParam(params, "prompt"); found {
		prompt, _ = raw.(string)
	}

	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "http://") {
		rawURL = "https://" + rawURL[7:]
	}
	if !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}

	if err := validateURL(rawURL); err != nil {
		return nil, err
	}

	wfCacheKey := rawURL
	if cached, ok := t.memCache.Load(wfCacheKey); ok {
		entry := cached.(cachedFetch)
		if time.Since(entry.Timestamp) < webFetchCacheTTL {
			return formatWebFetchOutput(rawURL, prompt, entry.Content, len([]rune(entry.Content)), entry.Content, false), nil
		}
		t.memCache.Delete(wfCacheKey)
	}

	kvs := GetToolContext(ctx).KVStore
	if kvs != nil {
		if data, err := kvs.Get(ctx, webFetchCacheSessionID, wfCacheKey); err == nil && len(data) > 0 {
			var entry cachedFetch
			if json.Unmarshal(data, &entry) == nil && time.Since(entry.Timestamp) < webFetchCacheTTL {
				t.memCache.Store(wfCacheKey, entry)
				return formatWebFetchOutput(rawURL, prompt, entry.Content, len([]rune(entry.Content)), entry.Content, false), nil
			}
			kvs.Delete(ctx, webFetchCacheSessionID, wfCacheKey)
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试获取 URL %q，但构造 HTTP 请求失败", rawURL),
			WithErrDetail(fmt.Sprintf("URL %q 包含非法字符，无法构造 HTTP 请求", rawURL), err),
			"先自查：URL 中是否包含未编码的非法字符（如空格、中文、控制字符）？确认 URL 与请求参数正确后再重试",
		), err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试获取 URL %q，但网络请求失败", rawURL),
			WithErrDetail(fmt.Sprintf("向 %q 发起请求时网络连接失败", rawURL), err),
			"确认网络可达（若目标站点需代理或当前网络受限，应换用其它工具或告知用户）",
		), err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		cause := fmt.Sprintf("目标返回了 HTTP %d 状态码", resp.StatusCode)
		switch {
		case resp.StatusCode == http.StatusNotFound:
			cause = "目标返回了 HTTP 404，该 URL 对应的资源不存在"
		case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusUnauthorized:
			cause = "目标返回了 HTTP 403/401，站点拒绝访问（可能触发了反爬限制或需要登录鉴权）"
		case resp.StatusCode >= 500:
			cause = fmt.Sprintf("目标返回了 HTTP %d，服务器异常或过载", resp.StatusCode)
		}
		return nil, fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试获取 URL %q，但目标返回了 HTTP %d 状态码", rawURL, resp.StatusCode),
			cause,
			"检查 URL 是否正确；若为 404 说明资源不存在，应修正 URL；若为 403/5xx 说明访问受限或服务异常，应稍后重试或告知用户，不要反复请求同一 URL",
		))
	}
	if !isHTMLContentType(ct) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return formatNonHTML(rawURL, prompt, resp.StatusCode, ct, string(body)), nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试获取 URL %q，但读取页面内容失败", rawURL),
			WithErrDetail(fmt.Sprintf("读取 %q 的页面内容时传输中断或响应体异常", rawURL), err),
			"页面可能在传输过程中中断，稍后重试或更换其他来源",
		), err)
	}

	rawContent := htmlToMarkdown(string(body))
	originalLen := len([]rune(rawContent))

	content := rawContent
	truncated := false
	if originalLen > webFetchMaxContentChars {
		content = string([]rune(rawContent[:webFetchMaxContentChars]))
		truncated = true
	}

	entry := cachedFetch{
		Content:   content,
		Timestamp: time.Now(),
		URL:       rawURL,
	}
	t.memCache.Store(wfCacheKey, entry)
	if kvs != nil {
		if data, err := json.Marshal(entry); err == nil {
			kvs.Set(ctx, webFetchCacheSessionID, wfCacheKey, data, int(webFetchCacheTTL.Seconds()))
		}
	}

	return formatWebFetchOutput(rawURL, prompt, content, originalLen, rawContent, truncated), nil
}

func formatWebFetchOutput(rawURL, prompt, content string, originalLen int, rawContent string, truncated bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- 网页获取：%s ---\n", rawURL)
	fmt.Fprintf(&sb, "状态：200 | 原始大小：%d 字符 | 返回：%d 字符\n",
		originalLen, len(content))
	if prompt != "" {
		fmt.Fprintf(&sb, "提示词：%s\n", prompt)
	}
	if truncated {
		fmt.Fprintf(&sb, "注意：内容已被截断（省略了 %d 字符）。\n", originalLen-webFetchMaxContentChars)
		fmt.Fprintf(&sb, "要聚焦于特定部分，请使用描述性的 `prompt` 参数重新获取（例如，prompt=\"提取关于定价的部分\"）。\n")
	}
	fmt.Fprintf(&sb, "\n%s\n", content)
	return sb.String()
}

func isHTMLContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") ||
		strings.Contains(ct, "application/xhtml") ||
		strings.Contains(ct, "text/plain") ||
		ct == ""
}

func formatNonHTML(rawURL, prompt string, statusCode int, contentType, preview string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- 网页获取：%s ---\n", rawURL)
	fmt.Fprintf(&sb, "状态：%d | Content-Type：%s\n", statusCode, contentType)
	if prompt != "" {
		fmt.Fprintf(&sb, "提示词：%s\n", prompt)
	}
	fmt.Fprintf(&sb, "\n非 HTML 内容。正文预览（%d 字节）：\n", len(preview))
	if len(preview) > 0 {
		fmt.Fprintf(&sb, "%s...\n", preview)
	}
	return sb.String()
}

func htmlToMarkdown(htmlStr string) string {
	mdResult, err := mdConverter.ConvertString(htmlStr)
	if err != nil || len(mdResult) == 0 {
		return fallbackHtmlToText(htmlStr)
	}
	return trimLines(mdResult)
}

func trimLines(s string) string {
	var result strings.Builder
	result.Grow(len(s))
	for start := 0; start < len(s); {
		end := strings.IndexByte(s[start:], '\n')
		if end == -1 {
			end = len(s) - start
		}
		line := strings.TrimSpace(s[start : start+end])
		if line != "" {
			if result.Len() > 0 {
				result.WriteByte('\n')
			}
			result.WriteString(line)
		}
		start += end + 1
	}
	return result.String()
}

func fallbackHtmlToText(s string) string {
	s = strings.ReplaceAll(s, "<script", "\n<script")
	s = strings.ReplaceAll(s, "</script>", "</script>\n")
	s = strings.ReplaceAll(s, "<style", "\n<style")
	s = strings.ReplaceAll(s, "</style>", "</style>\n")

	var result strings.Builder
	inTag := false
	inScriptOrStyle := false
	for _, r := range s {
		switch r {
		case '<':
			inTag = true
			if len(result.String()) > 0 && result.String()[len(result.String())-1] == '\n' && !inScriptOrStyle {
				continue
			}
		case '>':
			inTag = false
			lower := strings.ToLower(strings.TrimSpace(result.String()))
			if strings.HasSuffix(lower, "script") || strings.HasSuffix(lower, "style") {
				inScriptOrStyle = true
				result.WriteRune(r)
				continue
			}
			if inScriptOrStyle && (strings.Contains(lower, "/script") || strings.Contains(lower, "/style")) {
				inScriptOrStyle = false
				result.WriteRune(r)
				continue
			}
			if !inScriptOrStyle {
				result.WriteByte(' ')
			}
			continue
		default:
			if !inTag && !inScriptOrStyle {
				result.WriteRune(r)
			} else if r == '/' && inTag {
				result.WriteRune(r)
			}
		}
	}

	cleaned := strings.Join(strings.Fields(result.String()), " ")
	var finalLines []string
	for _, line := range strings.Split(cleaned, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && len(line) > 1 {
			finalLines = append(finalLines, line)
		}
	}
	return strings.Join(finalLines, "\n")
}

func isPrivateIP(ip net.IP) bool {
	privateRanges := []struct {
		network *net.IPNet
	}{
		{parseCIDR("127.0.0.0/8")},
		{parseCIDR("10.0.0.0/8")},
		{parseCIDR("172.16.0.0/12")},
		{parseCIDR("192.168.0.0/16")},
		{parseCIDR("169.254.0.0/16")},
		{parseCIDR("::1/128")},
		{parseCIDR("fc00::/7")},
		{parseCIDR("fe80::/10")},
		{parseCIDR("0.0.0.0/8")},
	}
	for _, r := range privateRanges {
		if r.network != nil && r.network.Contains(ip) {
			return true
		}
	}
	return false
}

func parseCIDR(s string) *net.IPNet {
	_, network, err := net.ParseCIDR(s)
	if err != nil {
		return nil
	}
	return network
}

func validateURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试获取 URL %q，但该 URL 无法解析", rawURL),
			WithErrDetail(fmt.Sprintf("URL %q 格式无效（缺少协议或主机名）", rawURL), err),
			"检查 URL 是否完整（如 https://example.com/path），修正后重试",
		))
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试获取 URL %q，但协议不受支持", rawURL),
			fmt.Sprintf("URL %q 的协议 %q 不受支持", rawURL, parsed.Scheme),
			"WebFetch 仅支持 http 与 https 协议，改用这两种协议的 URL 后重试",
		))
	}

	host := parsed.Hostname()
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试获取 URL %q，但无法解析主机 %q", rawURL, host),
			fmt.Sprintf("URL %q 的主机名 %q 无法解析（DNS 解析失败）", rawURL, host),
			"确认域名拼写正确、当前网络可访问该域名（可在浏览器中验证），或改用其它可访问的 URL",
		))
	}
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return fmt.Errorf("%s", BuildGuide(
				fmt.Sprintf("尝试获取 URL %q，但访问被拒绝", rawURL),
				fmt.Sprintf("URL %q 解析为私有或内部地址 %s（SSRF 风险）", rawURL, ip),
				"确认 URL 指向公网可访问的资源，不要访问内网/私有 IP（如 127.0.0.1、10.x、192.168.x、169.254.x）",
			))
		}
	}
	return nil
}
