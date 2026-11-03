package sessionruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/morehao/golib/gerror"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/domain/session"
)

type fakeResolver struct {
	out []session.ResolvedAttachment
	err error
}

func (f fakeResolver) Resolve(context.Context, []string) ([]session.ResolvedAttachment, error) {
	return f.out, f.err
}

// Link 记录调用（link 测试用；无断言时忽略）。nil 指针安全。
func (f fakeResolver) Link(context.Context, []string, string) error { return nil }

// TestBuildPromptPartsNoAttachments 无附件 → 单 text part（行为与旧 Prompt 逐字一致）。
func TestBuildPromptPartsNoAttachments(t *testing.T) {
	s := &Service{}
	parts, _, err := s.buildPromptParts(context.Background(), "s1", "hello", nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(parts) != 1 || parts[0].Kind != protocol.PromptPartText || parts[0].Text != "hello" {
		t.Fatalf("无附件应单 text part，got %+v", parts)
	}
}

// TestBuildPromptPartsMemoryMode 有附件但 resolver 为 nil（memory 模式）→ 40501。
func TestBuildPromptPartsMemoryMode(t *testing.T) {
	s := &Service{} // attachments nil
	_, _, err := s.buildPromptParts(context.Background(), "s1", "hi", []string{"att_x"})
	if err == nil || !isNotSupported(err) {
		t.Fatalf("memory 模式应 40501，got %v", err)
	}
}

// TestBuildPromptPartsResolveError 解析失败（附件缺失）→ 错误透传（零副作用：未触达 orch）。
func TestBuildPromptPartsResolveError(t *testing.T) {
	s := &Service{attachments: fakeResolver{err: errors.New("attachment_not_found")}}
	_, _, err := s.buildPromptParts(context.Background(), "s1", "hi", []string{"att_missing"})
	if err == nil {
		t.Fatal("附件缺失应报错")
	}
}

// TestBuildPromptPartsImageGateRejects 带 image 但门禁判不出支持（orch/planes 为 nil）→ model_no_image_input。
func TestBuildPromptPartsImageGateRejects(t *testing.T) {
	s := &Service{attachments: fakeResolver{out: []session.ResolvedAttachment{
		{ID: "att_img", Kind: session.AttachmentKindImage, MimeType: "image/png", SizeBytes: 4, Data: []byte{1, 2, 3, 4}},
	}}}
	_, _, err := s.buildPromptParts(context.Background(), "s1", "看图", []string{"att_img"})
	if err == nil || !strings.Contains(err.Error(), "model_no_image_input") {
		t.Fatalf("图门禁不过应 model_no_image_input，got %v", err)
	}
}

// TestAssemblePartsSmallImage 小图 → text part + image part（Data 透传）。
func TestAssemblePartsSmallImage(t *testing.T) {
	resolved := []session.ResolvedAttachment{
		{ID: "att_img", Kind: session.AttachmentKindImage, MimeType: "image/png", Data: []byte{1, 2, 3}},
	}
	parts := assembleParts(resolved, "看图")
	if len(parts) != 2 {
		t.Fatalf("应 2 个 part，got %d", len(parts))
	}
	if parts[0].Kind != protocol.PromptPartText || parts[0].Text != "看图" {
		t.Fatalf("首段应是原文 text：%+v", parts[0])
	}
	if parts[1].Kind != protocol.PromptPartImage || parts[1].AttachmentID != "att_img" || len(parts[1].Data) != 3 {
		t.Fatalf("次段应是 image part 且带 Data：%+v", parts[1])
	}
}

// TestAssemblePartsDocumentHandle 文档 / 大图 → 原文 text 段 + 独立 handle text 段（无 image part）。
//
// handle 必须**单独成段**：下游 composeDeliverParts 会整段替换第一个 text 段（叠首轮前缀），
// 拼在原文里会被冲掉（2026-11-03 实测的丢附件回归）。
func TestAssemblePartsDocumentHandle(t *testing.T) {
	resolved := []session.ResolvedAttachment{
		{ID: "att_doc", Kind: session.AttachmentKindDocument, Name: "a.txt", SizeBytes: 5,
			Sha256: "0123456789abcdef0000", ReadPath: "/abs/attachments/data/x/a.txt"},
	}
	parts := assembleParts(resolved, "读文件")
	if len(parts) != 2 {
		t.Fatalf("文档应产 2 个 text part（原文 + handle），got %d: %+v", len(parts), parts)
	}
	if parts[0].Kind != protocol.PromptPartText || parts[0].Text != "读文件" {
		t.Fatalf("首段应是原文，got %+v", parts[0])
	}
	if parts[1].Kind != protocol.PromptPartText {
		t.Fatalf("次段应是 handle text，got %+v", parts[1])
	}
	if !strings.Contains(parts[1].Text, "[附件 a.txt size=5 sha256:0123456789abcdef path=/abs/attachments/data/x/a.txt]") {
		t.Fatalf("次段应含 handle 行，got %q", parts[1].Text)
	}
}

func isNotSupported(err error) bool {
	var ge gerror.Error
	if errors.As(err, &ge) {
		return ge.Code == 40501
	}
	return false
}

// recordingResolver 记录 Link 的入参，用于断言 link 时机（只在受理后、且带附件时被调用）。
type recordingResolver struct {
	linkCalls int
	lastIDs   []string
	lastSID   string
}

func (r *recordingResolver) Resolve(context.Context, []string) ([]session.ResolvedAttachment, error) {
	return nil, nil
}
func (r *recordingResolver) Link(_ context.Context, ids []string, sid string) error {
	r.linkCalls++
	r.lastIDs = ids
	r.lastSID = sid
	return nil
}

// TestLinkAttachments 钉住 link 时机：仅当有附件且有 resolver 时调用 Link，且透传正确。
func TestLinkAttachments(t *testing.T) {
	// 有附件 + resolver → 调一次，透传 ids/session
	r := &recordingResolver{}
	s := &Service{attachments: r}
	if err := s.linkAttachments(context.Background(), "ses_x", []string{"att_a", "att_b"}); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if r.linkCalls != 1 || r.lastSID != "ses_x" || len(r.lastIDs) != 2 {
		t.Fatalf("期望调一次并透传，实得 calls=%d sid=%q ids=%v", r.linkCalls, r.lastSID, r.lastIDs)
	}

	// 无附件 → 不调
	r2 := &recordingResolver{}
	(&Service{attachments: r2}).linkAttachments(context.Background(), "ses_y", nil)
	if r2.linkCalls != 0 {
		t.Fatalf("无附件不应调 Link，实得 %d", r2.linkCalls)
	}

	// resolver 为 nil（memory 模式）→ 不 panic 不调
	if err := (&Service{}).linkAttachments(context.Background(), "ses_z", []string{"att_a"}); err != nil {
		t.Fatalf("nil resolver 应 no-op，实得 err=%v", err)
	}
}
