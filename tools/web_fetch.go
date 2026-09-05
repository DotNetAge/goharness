package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/DotNetAge/goharness/logging"
	"github.com/DotNetAge/goharness/sandbox"
)

// webFetchLargePageLines 是判定页面需要落盘的行数阈值。
// 超过此行数的页面落盘到 SessionDir/.webfetch/，Agent 用 Read 的 offset/limit 分页读取，
// 避免一次性塞满上下文。阈值取 500 行：与 Read 的默认行数一致——
// Read 单次默认最多读 500 行，WebFetch 同样以 500 行作为"直返/落盘"分界，
// 两端阈值统一，Agent 用 Read 读落盘文件时一次恰好读完一屏，语义对齐。
const webFetchLargePageLines = 500

// webFetchCacheTTL 是落盘文件的有效期。命中且未过期时跳过网络抓取。
const webFetchCacheTTL = 24 * time.Hour

// webFetchMaxFileBytes 是落盘文件的最大字节数。
// 受 Read 工具 MaxSizeBytes=256KB 硬限制约束（Read 在分页前先做大小检查），
// 留 16KB 安全边距，避免 UTF-8 多字节字符在边界处把文件顶过 256KB。
const webFetchMaxFileBytes = 240 * 1024

// webFetchMaxContentChars 是 SessionDir 不可用时回退截断的字符数上限。
const webFetchMaxContentChars = 50000

// webFetchDirName 是 SessionDir 下存放 WebFetch 落盘文件的子目录名。
// 以点开头：不污染 Agent 的 Ls 视图（默认不显示隐藏目录）。
const webFetchDirName = ".webfetch"

// WebFetchTool 实现了网页内容获取工具。
// 用于获取并转换网页内容为 Markdown 格式，具有以下安全特性：
//   - SSRF 防护：阻止访问内网 IP 地址
//   - URL 验证：只允许 http/https 协议
//   - 重定向限制：最多 10 次重定向
//   - 内容大小限制：最大 10MB 响应体
//
// 内容处理（工具正交：WebFetch 管抓取落盘，Read 管分页读取）：
//   - HTML → Markdown 转换
//   - 小页面（≤500 行）：直接返回 Markdown 内容
//   - 大页面（>500 行）：落盘到 SessionDir/.webfetch/，返回路径提示 Agent 用 Read 分页读取
//   - 非 HTML 内容：原样返回预览
type WebFetchTool struct {
	client *stealthClient // 隐蔽客户端（TLS 指纹伪装 + SSRF 防护 + 重试退避）
}

// NewWebFetchTool 创建一个 WebFetchTool 实例。
// 通过 NewStealthClient 统一处理 TLS 指纹伪装、cookiejar 会话、重试退避与 SSRF 防护，
// 替代旧实现的手搓 *http.Client + DialContext。SSRF 双层防护保留：
//   - 沙箱 CheckURL 预检（Execute 内，含 DNS 解析与网段检查）
//   - 拨号层 ssrfDialContext（stealthClient 内，复用 isPrivateIP，防 DNS rebinding）
//
// 返回：
//   - FuncTool: 配置好的 WebFetchTool 实例
func NewWebFetchTool(logger logging.Logger) FuncTool {
	c, _ := NewStealthClient(logger)
	return &WebFetchTool{client: c}
}

// Info 返回 WebFetch 工具的元信息。
func (t *WebFetchTool) Info() *ToolInfo {
	return &ToolInfo{
		Name: "WebFetch",
		// -1 禁用 executor 的通用截断：WebFetch 自行管理内容形态——
		// 小页面直接返回（≤500 行），大页面落盘并返回路径提示（几十字符）。
		// executor 再截断会破坏大页面的路径提示语义。唯一内容控制由 Execute 内完成。
		MaxResultSizeChars: -1,
		Description:        "获取网页内容并转为 Markdown。",
		Prompt:             `获取网页内容并转为 Markdown。`,
		Tags:               []string{"web", "fetch", "url", "content", "http"},
		IsReadOnly:         true,
		Parameters: []Parameter{
			{
				Name:        "url",
				Type:        "string",
				Description: "要获取的 URL。",
				Required:    true,
			},
		},
	}
}

// webFetchFileName 把 URL 转成 sha256 摘要文件名（不含目录）。
// 用 sha256 而非 URL sanitize：URL 可能极长（超过 255 字节文件名上限），
// sanitize 后的名称仍可能因截断而冲突或不可读；sha256 产生固定 64 字符摘要，
// 加 .md 后缀共 67 字节，稳定、唯一、无冲突、无非法字符。
// 相同 URL（经规范化）必产生相同文件名（缓存命中）；不同 URL 摘要几乎不可能碰撞。
// 调用前须先完成 URL 规范化（http→https、补前缀），
// 确保 example.com / http://example.com / https://example.com 命中同一缓存。
func webFetchFileName(rawURL string) string {
	h := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(h[:]) + ".md"
}

// webFetchCachePath 返回 URL 对应的落盘绝对路径与 .webfetch 目录路径。
// sessionDir 为空时（测试 mock 或未配置持久化）返回 ("","")，表示无可用落盘位置。
func webFetchCachePath(sessionDir, rawURL string) (filePath, dirPath string) {
	if sessionDir == "" {
		return "", ""
	}
	dirPath = filepath.Join(sessionDir, webFetchDirName)
	filePath = filepath.Join(dirPath, webFetchFileName(rawURL))
	return
}

// countLines 统计 Markdown 行数（与 Read 的 cat -n 行号语义一致）。
// 末尾无换行符的最后一行也计入。
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// truncateMarkdownToBytes 按 webFetchMaxFileBytes 截断 Markdown，rune 安全。
// 截断位置回退到上一个 \n 边界，避免切断段落/行。返回截断后的内容与是否截断。
func truncateMarkdownToBytes(content string) (string, bool) {
	if len(content) <= webFetchMaxFileBytes {
		return content, false
	}
	// 按字节切到上限，再回退到 rune 边界（避免切坏多字节字符）
	cut := webFetchMaxFileBytes
	for cut > 0 && !utf8.RuneStart(content[cut]) {
		cut--
	}
	truncated := content[:cut]
	// 回退到最后一个换行符，避免切坏行
	if i := strings.LastIndexByte(truncated, '\n'); i > 0 {
		truncated = truncated[:i]
	}
	return truncated, true
}

// validateWebFetchURL 提取并规范化 URL 参数（强制 HTTPS）。
func validateWebFetchURL(params map[string]any) (string, error) {
	rawURL, err := ValidateRequiredString("WebFetch", params, "url")
	if err != nil {
		return "", err
	}
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "http://") {
		rawURL = "https://" + rawURL[7:]
	}
	if !strings.HasPrefix(rawURL, "https://") {
		rawURL = "https://" + rawURL
	}
	return rawURL, nil
}

// authorizeWebFetch 对 URL 做 SSRF 安全校验（带会话授权豁免）。
// SSRF 网段策略统一由沙箱 EnforceURLWithWhitelist 决策（含 DNS 解析与网段检查）：
// 命中内网网段且会话白名单已授权该 host（用户此前 AllowSession 记忆）→ 放行；
// 未授权的 AskUser 与硬性禁止（Deny：解析失败等）→ 拒绝。
// 沙箱预检通过后，拨号层 DialContext 拦截器仍会做强制检查（防 DNS rebinding，
// 同样豁免会话授权 host）。未注入沙箱时拒绝执行（安全决策统一收口到沙箱）。
func authorizeWebFetch(ctx context.Context, rawURL string) error {
	sb, err := requireSandbox(ctx, "WebFetch")
	if err != nil {
		return err
	}
	dec := sb.EnforceURLWithWhitelist(rawURL, sessionNetworkHosts(ctx))
	if dec.Decision == sandbox.DecisionDeny || dec.Decision == sandbox.DecisionAskUser {
		return fmt.Errorf("%s", dec.Reason)
	}
	return nil
}

// Grant 实现 PermissionRequired：URL 命中内网网段（AskUser）且会话白名单未授权时
// 触发用户授权弹窗（granted=false）。
// 硬性禁止（Deny：URL 解析失败等）与参数缺失放行（弹窗无意义，让 Execute 报错）；
// 公网/回环 URL 与会话白名单已授权的 host 直接放行。
// 会话未注入沙箱时放行，由 Execute 阶段统一拒绝（配置错误，授权无意义）。
func (t *WebFetchTool) Grant(ctx context.Context, params map[string]any) (bool, string) {
	rawURL, err := validateWebFetchURL(params)
	if err != nil {
		return true, ""
	}

	tc := GetToolContext(ctx)
	if tc == nil || tc.Session == nil || tc.Session.Sandbox() == nil {
		return true, ""
	}

	dec := tc.Session.Sandbox().EnforceURLWithWhitelist(rawURL, sessionNetworkHosts(ctx))
	switch dec.Decision {
	case sandbox.DecisionDeny, sandbox.DecisionAskUser:
		return false, dec.Reason
	}
	return true, ""
}

// performWebFetch 执行网页内容获取核心逻辑：缓存检查、HTTP 请求、内容处理与输出。
func performWebFetch(ctx context.Context, t *WebFetchTool, logger logging.Logger, rawURL, filePath, dirPath string) (string, error) {
	// 文件缓存命中检查：文件存在且 mtime 在 24h 内 → 跳过网络，按行数决定返回形态。
	if filePath != "" {
		if info, statErr := os.Stat(filePath); statErr == nil && time.Since(info.ModTime()) < webFetchCacheTTL {
			if data, readErr := os.ReadFile(filePath); readErr == nil {
				content := string(data)
				lines := countLines(content)
				if lines <= webFetchLargePageLines {
					return formatWebFetchOutput(rawURL, content, len([]rune(content)), false), nil
				}
				return formatWebFetchPathOutput(rawURL, filePath, lines, len(content), false), nil
			}
			if logger != nil {
				logger.Warn("读取 WebFetch 缓存文件失败，将重新抓取", "path", filePath)
			}
		}
	}

	// 把会话授权的 host 列表注入请求 ctx：拨号层 ssrfDialContext 与重定向校验
	// validateURL 从 ctx 读取，对授权 host 跳过内网网段拦截（用户已确认可访问），
	// 保证"授权后能真正访问"——预检放行而拨号层拦截会让授权形同虚设。
	ctx = WithAuthorizedHosts(ctx, sessionNetworkHosts(ctx))

	resp, err := t.client.Do(ctx, StealthRequest{
		Method:          "GET",
		URL:             rawURL,
		FollowRedirects: true,
	})
	if err != nil {
		return "", fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试获取 URL %q，但网络请求失败", rawURL),
			WithErrDetail(fmt.Sprintf("向 %q 发起请求时网络连接失败", rawURL), err),
			"确认网络可达（若目标站点需代理或当前网络受限，应换用其它工具或告知用户）",
		), err)
	}
	defer resp.Body.Close()

	ct := resp.Header("Content-Type")
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
		return "", fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试获取 URL %q，但目标返回了 HTTP %d 状态码", rawURL, resp.StatusCode),
			cause,
			"检查 URL 是否正确；若为 404 说明资源不存在，应修正 URL；若为 403/5xx 说明访问受限或服务异常，应稍后重试或告知用户，不要反复请求同一 URL",
		))
	}
	if !isHTMLContentType(ct) {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return formatNonHTML(rawURL, resp.StatusCode, ct, string(body)), nil
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("%s（原始错误：%w）", BuildGuide(
			fmt.Sprintf("尝试获取 URL %q，但读取页面内容失败", rawURL),
			WithErrDetail(fmt.Sprintf("读取 %q 的页面内容时传输中断或响应体异常", rawURL), err),
			"页面可能在传输过程中中断，稍后重试或更换其他来源",
		), err)
	}

	rawContent := htmlToMarkdown(string(body))
	originalLen := len([]rune(rawContent))
	lines := countLines(rawContent)

	// 小页面（≤500 行）：直接返回 Markdown 内容，不落盘。
	if lines <= webFetchLargePageLines {
		return formatWebFetchOutput(rawURL, rawContent, originalLen, false), nil
	}

	// 大页面（>500 行）：落盘到 SessionDir/.webfetch/，返回路径提示 Agent 用 Read 分页读取。
	// SessionDir 不可用（测试 mock 或未配置持久化）时回退到旧的字符截断策略，不让大页面无路可走。
	if filePath == "" {
		content := rawContent
		truncated := false
		if originalLen > webFetchMaxContentChars {
			content = string([]rune(rawContent[:webFetchMaxContentChars]))
			truncated = true
		}
		if logger != nil {
			logger.Warn("SessionDir 不可用，WebFetch 回退到字符截断返回（无法落盘分页）", "url", rawURL)
		}
		return formatWebFetchOutput(rawURL, content, originalLen, truncated), nil
	}

	// 创建 .webfetch 目录
	if err := os.MkdirAll(dirPath, 0755); err != nil {
		return "", fmt.Errorf("创建 WebFetch 缓存目录失败: %w", err)
	}

	// 截断到 240KB（Read 的 256KB 硬限制约束），超限追加注释行说明
	storedContent, byteTruncated := truncateMarkdownToBytes(rawContent)
	if byteTruncated {
		storedContent += fmt.Sprintf(
			"\n\n<!-- 已截断：原始 %d 字节，存储 %d 字节。 -->\n",
			len(rawContent), len(storedContent))
	}

	// 写文件（覆盖式，缓存过期后重新抓取直接覆盖）
	if err := os.WriteFile(filePath, []byte(storedContent), 0644); err != nil {
		return "", fmt.Errorf("写入 WebFetch 缓存文件失败: %w", err)
	}

	// SessionDir 位于沙箱 AllowedDirs（如 ~/.mindx）子树内，isOutsideWorkspace 默认放行，
	// Read 的 EnforceFile/Grant 无需 WebFetch 手动打补丁——工具正交：WebFetch 只管落盘。

	if logger != nil {
		logger.Info("WebFetch 落盘完成",
			"url", rawURL, "file", filePath,
			"lines", lines, "bytes", len(storedContent), "byte_truncated", byteTruncated)
	}

	return formatWebFetchPathOutput(rawURL, filePath, lines, len(storedContent), byteTruncated), nil
}

// Execute 编排 WebFetch 工具执行流程：validate → authorize → perform。
func (t *WebFetchTool) Execute(ctx context.Context, params map[string]any) (any, error) {
	rawURL, err := validateWebFetchURL(params)
	if err != nil {
		return nil, err
	}

	if err := authorizeWebFetch(ctx, rawURL); err != nil {
		return nil, err
	}

	// 获取 Session 与落盘路径；logger 从 ToolContext 注入（禁止内部创建日志）。
	tc := GetToolContext(ctx)
	var logger logging.Logger
	if tc != nil {
		logger = tc.Logger
	}
	var filePath, dirPath string
	if tc != nil && tc.Session != nil {
		filePath, dirPath = webFetchCachePath(tc.Session.SessionDir(), rawURL)
	}

	return performWebFetch(ctx, t, logger, rawURL, filePath, dirPath)
}

// formatWebFetchOutput 格式化小页面（≤500 行）或回退截断场景的输出。
func formatWebFetchOutput(rawURL, content string, originalLen int, truncated bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- 网页获取：%s ---\n", rawURL)
	fmt.Fprintf(&sb, "状态：200 | 原始大小：%d 字符 | 返回：%d 字符\n",
		originalLen, len([]rune(content)))
	if truncated {
		fmt.Fprintf(&sb, "注意：内容已被截断（省略了 %d 字符）。\n", originalLen-webFetchMaxContentChars)
	}
	fmt.Fprintf(&sb, "\n%s\n", content)
	return sb.String()
}

// formatWebFetchPathOutput 生成大页面（>500 行）落盘后的引导输出。
// 用绝对路径，匹配 ResolveTargetPath 对绝对路径直通的语义。
// 文案保持简洁：WebFetch 只负责落盘并告知路径，分页读法由 Read 的 Prompt 说明，
// 不在 WebFetch 侧重复，保持工具正交。
func formatWebFetchPathOutput(rawURL, filePath string, lines, bytes int, byteTruncated bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- 网页获取：%s ---\n", rawURL)
	fmt.Fprintf(&sb, "状态：200 | 行数：%d | 字节数：%d\n", lines, bytes)
	if byteTruncated {
		fmt.Fprintf(&sb, "注意：原始页面超过 240KB，已截断存储（Read 工具的单文件 256KB 上限约束）。\n")
	}
	fmt.Fprintf(&sb, "\n文件已下载到：%s\n", filePath)
	fmt.Fprintf(&sb, "请用 Read 工具读取上述路径（绝对路径）以获取搜索内容。\n")
	return sb.String()
}

func isHTMLContentType(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.Contains(ct, "text/html") ||
		strings.Contains(ct, "application/xhtml") ||
		strings.Contains(ct, "text/plain") ||
		ct == ""
}

func formatNonHTML(rawURL string, statusCode int, contentType, preview string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- 网页获取：%s ---\n", rawURL)
	fmt.Fprintf(&sb, "状态：%d | Content-Type：%s\n", statusCode, contentType)
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
		// 回环（127.0.0.0/8、::1/128）不在列表：mindx 是单机桌面应用，
		// 访问本机服务（本地开发服务器、CDP 调试端口）是正常行为，
		// 预检层（沙箱 CheckURL）与拨号层同步放行回环。
		{parseCIDR("10.0.0.0/8")},
		{parseCIDR("172.16.0.0/12")},
		{parseCIDR("192.168.0.0/16")},
		{parseCIDR("169.254.0.0/16")},
		{parseCIDR("100.64.0.0/10")}, // CGNAT（含阿里云元数据 100.100.100.200）
		{parseCIDR("192.0.0.0/24")},  // IETF 协议分配
		{parseCIDR("198.18.0.0/15")}, // 基准测试网络
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

// validateURL 校验 URL 可达性与目标 IP 安全性（当前仅用于 stealth_client 重定向校验）。
// 会话授权主机（ctx 注入）跳过私有网段拦截——与拨号层豁免一致，保证授权链路完整。
func validateURL(ctx context.Context, rawURL string) error {
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
	authorized := hostAuthorized(ctx, host)
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%s", BuildGuide(
			fmt.Sprintf("尝试获取 URL %q，但无法解析主机 %q", rawURL, host),
			fmt.Sprintf("URL %q 的主机名 %q 无法解析（DNS 解析失败）", rawURL, host),
			"确认域名拼写正确、当前网络可访问该域名（可在浏览器中验证），或改用其它可访问的 URL",
		))
	}
	for _, ip := range ips {
		if !authorized && isPrivateIP(ip) {
			return fmt.Errorf("%s", BuildGuide(
				fmt.Sprintf("尝试获取 URL %q，但访问被拒绝", rawURL),
				fmt.Sprintf("URL %q 解析为私有或内部地址 %s（SSRF 风险）", rawURL, ip),
				"确认 URL 指向公网可访问的资源，不要访问内网/私有 IP（如 10.x、192.168.x、169.254.x）",
			))
		}
	}
	return nil
}
