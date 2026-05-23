package tools

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type SogouAdapter struct {
	client *http.Client
}

func NewSogouAdapter() *SogouAdapter {
	return &SogouAdapter{
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (a *SogouAdapter) Name() string { return "sogou" }

func (a *SogouAdapter) Search(ctx context.Context, query string, opts SearchOptions) ([]SearchResult, error) {
	params := url.Values{}
	params.Set("query", query)
	params.Set("page", "1")
	num := opts.MaxResults
	if num > 10 {
		num = 10
	}
	params.Set("num", strconv.Itoa(num))

	reqURL := "https://www.sogou.com/web?" + params.Encode()
	md, err := fetchAndExtract(ctx, a.client, reqURL, nil)
	if err != nil {
		return nil, err
	}

	results := extractSearchResultsFromMarkdown(md, opts.MaxResults)
	for i := range results {
		if resolved := resolveSogouURL(results[i].URL); resolved != "" {
			results[i].URL = resolved
		}
	}
	return filterResults(results, opts), nil
}

func resolveSogouURL(href string) string {
	if href == "" {
		return ""
	}
	if encodedURL := extractURLParam(href); encodedURL != "" {
		return encodedURL
	}
	if strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	return ""
}

func extractURLParam(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	encodedURL := u.Query().Get("url")
	if encodedURL == "" {
		return ""
	}
	decoded, err := url.QueryUnescape(encodedURL)
	if err != nil {
		return ""
	}
	if strings.HasPrefix(decoded, "http://") || strings.HasPrefix(decoded, "https://") {
		return decoded
	}
	return ""
}
