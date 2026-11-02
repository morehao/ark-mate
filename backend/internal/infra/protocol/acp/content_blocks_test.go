package acp

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/morehao/ark-mate/internal/core/protocol"
)

// TestBuildContentBlocksImageBase64 P3：image part → ImageBlock，Data 编成合法 base64。
func TestBuildContentBlocksImageBase64(t *testing.T) {
	s := &Session{}
	raw := []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10} // JPEG 头若干字节
	parts := []protocol.PromptPart{
		{Kind: protocol.PromptPartText, Text: "看看这张图"},
		{Kind: protocol.PromptPartImage, AttachmentID: "att_x", MimeType: "image/jpeg", Data: raw},
	}
	blocks, err := s.buildContentBlocks(context.Background(), parts)
	if err != nil {
		t.Fatalf("buildContentBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("应有 2 个内容块（text+image），got %d", len(blocks))
	}
	// 第 2 块是 image。
	if blocks[1].Image == nil {
		t.Fatalf("第 2 块应是 image block：%+v", blocks[1])
	}
	want := base64.StdEncoding.EncodeToString(raw)
	if blocks[1].Image.Data != want {
		t.Fatalf("image base64 不符：got %q want %q", blocks[1].Image.Data, want)
	}
	if blocks[1].Image.MimeType != "image/jpeg" {
		t.Fatalf("image mime 不符：%q", blocks[1].Image.MimeType)
	}
}

// TestBuildContentBlocksImageWithoutData 缺字节的 image part 明确报错（不静默丢块）。
func TestBuildContentBlocksImageWithoutData(t *testing.T) {
	s := &Session{}
	_, err := s.buildContentBlocks(context.Background(), []protocol.PromptPart{
		{Kind: protocol.PromptPartImage, AttachmentID: "att_x", MimeType: "image/png"},
	})
	if err == nil {
		t.Fatal("缺字节的 image part 应报错")
	}
}
