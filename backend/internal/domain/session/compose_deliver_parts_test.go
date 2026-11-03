package session

import (
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/protocol"
)

// TestComposeDeliverPartsKeepsHandleSegment 钉住：首轮前缀只替换**第一个** text 段（用户原文），
// 其后独立的 handle text 段与 image 段原样保留。
//
// 回归背景（2026-11-03 实测）：assembleParts 曾把文档/大图的 handle 行拼进原文段，composeDeliverParts
// 整段替换时把 handle 冲掉，模型收不到可读路径 → 静默丢附件。修法 = handle 单独成段（见
// sessionruntime.assembleParts），本测试锁定 composeDeliverParts 侧的「只换第一段」语义不被改坏。
func TestComposeDeliverPartsKeepsHandleSegment(t *testing.T) {
	parts := []protocol.PromptPart{
		{Kind: protocol.PromptPartText, Text: "读文件"},
		{Kind: protocol.PromptPartText, Text: "\n[附件 a.txt size=5 sha256:abc path=/tmp/a.txt]\n"},
		{Kind: protocol.PromptPartImage, AttachmentID: "att_img", MimeType: "image/png", Data: []byte{9}},
	}
	out := composeDeliverParts(parts, "前缀\n读文件")
	if len(out) != 3 {
		t.Fatalf("应保持 3 段，got %d: %+v", len(out), out)
	}
	if out[0].Kind != protocol.PromptPartText || out[0].Text != "前缀\n读文件" {
		t.Fatalf("第一段应被替换成叠前缀的原文，got %+v", out[0])
	}
	if out[1].Kind != protocol.PromptPartText || !strings.Contains(out[1].Text, "path=/tmp/a.txt") {
		t.Fatalf("第二段（handle）应原样保留，got %+v", out[1])
	}
	if out[2].Kind != protocol.PromptPartImage || out[2].AttachmentID != "att_img" {
		t.Fatalf("第三段（image）应原样保留，got %+v", out[2])
	}
}
