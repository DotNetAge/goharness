package tools

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/DotNetAge/goharness/logging"
)

// ToutiaoAdapter 使用头条搜索（so.toutiao.com）进行搜索。
// 头条独立于搜狗系风控，结果页 .result-content a 匹配 20 条结果，
// 链接是 /search/jump?url=encoded 跳转，url 参数即真实地址（无需请求重定向），
// 接入成本最低、结果最丰富，作为重要的兜底引擎。
type ToutiaoAdapter struct {
	client *stealthClient
}

func NewToutiaoAdapter(logger logging.Logger) *ToutiaoAdapter {
	return &ToutiaoAdapter{
		client: newSearchStealthClient(logger, 12*time.Second),
	}
}

func (a *ToutiaoAdapter) Name() string { return "toutiao" }

func (a *ToutiaoAdapter) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("keyword", query)
	// pd=information 取资讯类结果（最稳定、最丰富）；综合类含视频/小视频，噪声大
	params.Set("pd", "information")
	reqURL := "https://so.toutiao.com/search?" + params.Encode()

	body, err := fetchBody(ctx, a.client, reqURL, nil)
	if err != nil {
		return nil, err
	}

	// 头条结果链接是 /search/jump?...&url=https%3A%2F%2F... 跳转，
	// url 参数即真实地址，直接解析无需请求重定向（与 360 的 data-mdurl 同思路）
	return extractResultsWithGoquery(body, ".result-content a", "href", "https://so.toutiao.com", resolveToutiaoURL, opts.MaxResults), nil
}

// resolveToutiaoURL 从 /search/jump?url=encoded 的 url 参数提取真实 URL。
// 头条结果 a 标签 href 形如 /search/jump?aid=1455&jtoken=...&url=https%3A%2F%2F...，
// url 参数解码后是 article.zlink.toutiao.com 短链 + 海量追踪参数（gd_ext_json、
// schemeParams 等，单条 URL 可达数千字符）。真正的文章 URL 藏在其中 h5_url 参数里
// （如 https://toutiao.com/group/7052952759838114343/）。优先提取 h5_url 得到干净短链；
// 无 h5_url 时（如用户主页 profile.zjurl.cn）回退到 url 参数原值。
func resolveToutiaoURL(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	encoded := u.Query().Get("url")
	if encoded == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(encoded)
	if err != nil {
		return encoded
	}
	// decoded 形如 https://article.zlink.toutiao.com/J4dQM?...&h5_url=https://toutiao.com/group/xxx/&...
	// 从中提取 h5_url 参数得到干净文章直链
	if inner, err := url.Parse(decoded); err == nil {
		if h5 := inner.Query().Get("h5_url"); strings.HasPrefix(h5, "http://") || strings.HasPrefix(h5, "https://") {
			return h5
		}
	}
	return decoded
}
