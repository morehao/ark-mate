package svcattachment

import (
	"errors"
	"io"
	"strings"
)

// errTooLarge 读超上限的哨兵（readAllLimited 内部用）。
var errTooLarge = errors.New("attachment: too large")

// readAllLimited 读全 reader 的字节，最多 limit 字节；超限返回 errTooLarge（不静默截断）。
//
// 边读边计数：读满 limit 再探 1 字节，若还有内容 → 超限。这样既不会无边界占用内存，
// 也不会把一个被截断的坏字节流当完整文件落盘。
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		return io.ReadAll(r)
	}
	// 多读 1 字节用于判定"是否还有内容"。
	buf, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > limit {
		return nil, errTooLarge
	}
	return buf, nil
}

// probeType 按文件头（magic bytes）判定真实类型，**不信客户端声明**。
//
// 返回 (kind, mime)：
//   - 命中图片白名单（png/jpeg/webp/gif）→ ("image", 真实 mime)；
//   - 否则 → ("document", 尽力而为了 mime，探测不出就 application/octet-stream)；
//   - **声明是图片但字节不是四种之一** → ("invalid_image", "")，调用方据此拒（attachment_unsupported_type）。
//
// 关键：一个"声明成 image/png 但字节是随机数据"的文件必须被拒，因为它要进 ACP image 块，
// 必须真图。判据只看**字节**，声明值只在"要不要当图片校验"这一处决定走哪条分支。
func probeType(data []byte, declaredMime string) (kind, mime string) {
	real, ok := sniffImageMime(data)
	declaredIsImage := strings.HasPrefix(strings.ToLower(strings.TrimSpace(declaredMime)), "image/")
	if ok {
		return "image", real
	}
	if declaredIsImage {
		// 声明是图，但字节不是四类之一 → 伪装图，拒。
		return "invalid_image", ""
	}
	// 文档：不设类型白名单；尽力探测 MIME 供预览 Content-Type。
	return "document", sniffDocumentMime(data)
}

// sniffImageMime 按 magic bytes 识别四类图片；识别不出返回 ("", false)。
func sniffImageMime(b []byte) (string, bool) {
	switch {
	case len(b) >= 8 && b[0] == 0x89 && b[1] == 'P' && b[2] == 'N' && b[3] == 'G' &&
		b[4] == 0x0D && b[5] == 0x0A && b[6] == 0x1A && b[7] == 0x0A:
		return "image/png", true
	case len(b) >= 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return "image/jpeg", true
	case len(b) >= 6 && (string(b[0:6]) == "GIF87a" || string(b[0:6]) == "GIF89a"):
		return "image/gif", true
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp", true
	}
	return "", false
}

// sniffDocumentMime 尽力探测文档 MIME（只用于预览 Content-Type，不参与任何门禁）。
func sniffDocumentMime(b []byte) string {
	// 与 http.DetectContentType 同源的最小子集，避免为一个提示值引入大依赖。
	if len(b) >= 4 && b[0] == '%' && b[1] == 'P' && b[2] == 'D' && b[3] == 'F' {
		return "application/pdf"
	}
	// 粗略识别纯文本（前 512 字节无 NUL 且可打印/常见空白）。
	n := len(b)
	if n > 512 {
		n = 512
	}
	texty := true
	for i := 0; i < n; i++ {
		c := b[i]
		if c == 0 {
			texty = false
			break
		}
		if c < 0x09 || (c > 0x0D && c < 0x20) {
			texty = false
			break
		}
	}
	if texty {
		return "text/plain; charset=utf-8"
	}
	return "application/octet-stream"
}

// sanitizeName 清洗文件名：去控制字符、去路径分隔符（只留单段）、长度 ≤ 200。
//
// blob 按 sha256 存，文件名只作展示/写进 handle，**不落目录**——但仍清洗，避免展示面的
// 怪字符与 handle 文本被注入（换行/路径）。
func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	// 取最后一段（去任何目录前缀）。
	if i := strings.LastIndexAny(s, "/\\"); i >= 0 {
		s = s[i+1:]
	}
	// 去控制字符（含换行/制表/CR —— 它们会破坏 handle 行）。
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7F {
			return -1
		}
		return r
	}, s)
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}
