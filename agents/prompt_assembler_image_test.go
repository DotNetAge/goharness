package agents

import (
	"testing"

	"github.com/DotNetAge/goharness/session"
)

// 回归：图片-only 用户消息组装后必须保留 image 内容块（不能因无文本被过滤）。
func TestAssembleMessages_ImageOnlyUser(t *testing.T) {
	history := []session.Message{{
		Role:   "user",
		Images: []session.ImageBlock{{MediaType: "image/png", Base64Data: "AAAA"}},
	}}
	msgs := AssembleMessages(nil, history, "")
	if len(msgs) != 1 {
		t.Fatalf("消息数=%d", len(msgs))
	}
	if len(msgs[0].Content) != 1 || msgs[0].Content[0].Type != "image" {
		t.Fatalf("图片块丢失: %+v", msgs[0].Content)
	}
}
