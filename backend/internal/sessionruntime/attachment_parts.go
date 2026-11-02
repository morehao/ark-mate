package sessionruntime

import (
	"context"
	"fmt"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/domain/session"
)

// ——— 附件 → 内容块（v32 附件链路，sessionruntime 是**唯一**解析点）———
//
// 顺序（设计 §5.2，fail-closed）：先校验附件存在/归属、再判图片门禁，**任一不过即整条 prompt
// 零副作用拒收**（不建轮/不落字节/不拉进程）。全部通过后把附件编成内容块：
//   - 小图 → PromptPart{Kind:image, Data, MimeType}（ACP 层编 base64 直投）；
//   - 文档 / 大图 → 在 **text 段**末尾追加一行 handle 文本（name/size/sha256前16/path），
//     模型用 read/read_image 按需读（不写工作区）。
//
// G3：附件 id 一律经 `AttachmentResolver.Resolve` 解析，本层**不** import dao/store。

// buildPromptParts 解析附件并把它们编进 parts（text 段在前，image 段随后）。
// text 是用户原文（作为首个 text part）。无附件时返回 TextParts(text)（与无附件行为逐字一致）。
func (s *Service) buildPromptParts(ctx context.Context, sessionID, text string, attachments []string) ([]protocol.PromptPart, error) {
	// 无附件：纯文本（行为零变化）。
	if len(attachments) == 0 {
		// 即便无附件，若请求带了非法 id 也不该出现（空列表即无）。
		return protocol.TextParts(text), nil
	}
	if s.attachments == nil {
		return nil, code.Errorf(code.NotSupported, "attachments 需要 sqlite 持久层（memory 模式不支持附件）")
	}
	// 1) 解析（不存在 / 越权 / 已清理 → 整体报错，零副作用）。
	resolved, err := s.attachments.Resolve(ctx, attachments)
	if err != nil {
		return nil, code.FromError(err)
	}
	// 2) 图片门禁：有 image 附件时必须确认当前路由能收图（只读连接期事实 / 模型 input_modalities，
	//    绝不用静态 Caps.Images）——不能收图 → 建轮前拒，零副作用。
	hasImage := false
	for _, r := range resolved {
		if r.Kind == session.AttachmentKindImage {
			hasImage = true
			break
		}
	}
	if hasImage {
		if err := s.checkImageGate(ctx, sessionID); err != nil {
			return nil, err
		}
	}
	// 3) 组装：text 段（原文 + 文档/大图 handle 追行）在前，image 段随后。
	return assembleParts(resolved, text), nil
}

// assembleParts 把已解析的附件编成内容块（纯函数：不含门禁/解析，便于单测）。
// text 段在前（原文 + 文档/大图 handle 追行），小图 image 段随后。
func assembleParts(resolved []session.ResolvedAttachment, text string) []protocol.PromptPart {
	textPart := text
	var imageParts []protocol.PromptPart
	for _, r := range resolved {
		switch r.Kind {
		case session.AttachmentKindImage:
			if len(r.Data) > 0 {
				// 小图：直投 image 块（Data 由 ACP 层编 base64）。
				imageParts = append(imageParts, protocol.PromptPart{
					Kind: protocol.PromptPartImage, AttachmentID: r.ID, MimeType: r.MimeType, Data: r.Data,
				})
			} else {
				// 大图（超过直投上限）：降级为 handle 文本 + read_image。
				textPart += handleLine(r)
			}
		default:
			// 文档：handle 文本 + read。
			textPart += handleLine(r)
		}
	}
	parts := make([]protocol.PromptPart, 0, 1+len(imageParts))
	parts = append(parts, protocol.PromptPart{Kind: protocol.PromptPartText, Text: textPart})
	parts = append(parts, imageParts...)
	return parts
}

// handleLine 构造一行附件 handle 文本（设计 §5.3.1）。path 只在 text part 里存活，不进事件/落库。
func handleLine(r session.ResolvedAttachment) string {
	prefix := r.Sha256
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	return fmt.Sprintf("\n[附件 %s size=%d sha256:%s path=%s]\n", r.Name, r.SizeBytes, prefix, r.ReadPath)
}

// checkImageGate 判当前会话能否收图：连接期事实 `PromptImageSupported()` 优先（true/false/unknown）；
// unknown（无活跃连接）时回落到会话模型声明的 input_modalities；仍判不出则 fail-closed 拒收。
//
// **绝不**读静态 `Caps.Images`（G4 / ADR-14）。
func (s *Service) checkImageGate(ctx context.Context, sessionID string) error {
	if sup := s.PromptImageSupported(); sup != nil {
		if *sup {
			return nil
		}
		return code.Errorf(code.InvalidRequest, "model_no_image_input: 当前路由/模型不支持图片输入")
	}
	// 连接期事实取不到（无活跃连接）：用会话模型声明的输入模态判。
	if s.modelSupportsImage(ctx, sessionID) {
		return nil
	}
	return code.Errorf(code.InvalidRequest, "model_no_image_input: 当前模型未声明图片输入能力")
}

// modelSupportsImage 读会话所选模型行的 input_modalities 是否含 image（连接无关的声明事实）。
// 取不到会话 / 模型行时返回 false（fail-closed）。
func (s *Service) modelSupportsImage(ctx context.Context, sessionID string) bool {
	if s.sessionPlane == nil || s.planes == nil {
		return false
	}
	rec, ok := s.sessionPlane.Lookup(sessionID)
	if !ok || rec.ModelID == "" {
		return false
	}
	mods, ok := s.planes.Resources.ModelInputModalities(ctx, rec.ModelID)
	if !ok {
		return false
	}
	for _, mod := range mods {
		if mod == "image" {
			return true
		}
	}
	return false
}
