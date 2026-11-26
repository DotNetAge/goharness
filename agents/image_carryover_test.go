package agents

import (
	"testing"

	"github.com/DotNetAge/goharness/session"
)

// 挂起续跑会重建协调器：已注入上下文的图片无需再管，未注入的必须搬到新协调器，
// 否则 Read 读图后紧接着挂起（同一次迭代里并行发起 AskUser 等）会让图既没进
// 上下文、也不会再补。
func TestCarryOverImagesKeepsUninjected(t *testing.T) {
	old := &hookCoord{imageEntries: []imageEntry{
		{toolSeq: 0, toolName: "Read", blocks: []session.ImageBlock{{MediaType: "image/png", Base64Data: "AAA"}}, injected: true},
		{toolSeq: 1, toolName: "Read", blocks: []session.ImageBlock{{MediaType: "image/png", Base64Data: "BBB"}}},
	}}

	next := newHookCoord()
	next.carryOverImages(old)

	if len(next.imageEntries) != 1 {
		t.Fatalf("应搬来 1 条未注入图片，得到 %d", len(next.imageEntries))
	}
	if next.imageEntries[0].blocks[0].Base64Data != "BBB" {
		t.Fatalf("搬来的应是不带 injected 的那条，得到 %+v", next.imageEntries[0])
	}

	// 搬过来之后仍能正常注入，并在这次的上下文里出现。
	adapter := &loopHookAdapter{coord: next}
	appended := adapter.takeVisionMessages()
	if len(appended) != 1 {
		t.Fatalf("应对搬来的图片生成 1 条多模态消息，得到 %d", len(appended))
	}
	if !next.imageEntries[0].injected {
		t.Fatalf("注入后应标记 injected，避免下次重复注入")
	}
}
