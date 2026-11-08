package assembly

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/domain/artifact"
	"github.com/morehao/ark-mate/internal/domain/session"
	"github.com/morehao/ark-mate/internal/dto/dtoartifact"
	"github.com/morehao/ark-mate/internal/dto/dtomaterial"
	"github.com/morehao/ark-mate/internal/infra/attachmentstore"
	"github.com/morehao/ark-mate/internal/infra/workartifact"
	"github.com/morehao/ark-mate/internal/persistence/model"
	"github.com/morehao/ark-mate/internal/persistence/store"
	"github.com/morehao/ark-mate/internal/service/svcartifact"
	"github.com/morehao/ark-mate/internal/service/svcmaterial"
	"github.com/morehao/golib/glog"
)

// defaultMaxSnapshotBytes 默认快照上限 32 MiB（方案 §4 已拍板 R2：超阈值**跳过不登记** + WARN）。
const defaultMaxSnapshotBytes int64 = 32 << 20

// artifactExtractor 实现 session.ArtifactExtractor：从一次工具事件里旁路抽取「`present` 声明的
// 最终交付物」为一条 artifacts 产物（file 类，字节快照进 filestore）。
//
// **只认 present（与 dsh 语义对齐）**：dsh 里 `write`/`edit` 只是「写文件动作」，其产出（produced）
// dsh 仅用于解析正文行内引用，**从不当作交付物**；`present` 才是模型「显式声明这是成品」的唯一动作
// （见 dsh `packages/deliverables/tool-present`）。早先平台把每次 write/edit 落文件都登记为产物并
// 自动收录进资料库，导致 `gen.py` 之类过程脚本与成品一起进了「本轮产物」和资料库。修正后只认 present。
//
// **严格两段式（方案 §2.5 C5）**：
//   - 同步段（在 ExtractToolWrite 返回前完成）：解析路径 → 读工作区文件字节进内存 buffer；
//     这是「捕获」——必须在终态帧这一刻做完，否则 dsh 下一步可能覆盖同名文件。
//   - 异步段（goroutine）：对 buffer 算 sha256 → 上传 filestore 拿 file_id → 建 artifacts 行。
//
// **零侵入（C1–C4）**：只读 dsh 已上报的事件、只读工作区文件、只写平台自己的 filestore/artifacts；
// 不 import 底座、不 spawn、不写工作区；任何失败只 WARN 不上抛。
//
// **自动收录（2026-11-04 产品拍板）**：产物登记成功后顺手把它收录进资料库的「产物」分栏
// （`category='artifact'`，引用而非复制）。早先的口径是「只登记、收录要用户显式点」，真机上
// 用户不会主动去点——产物生成了、资料库里却永远没有。自动收录失败只 WARN：产物行已经在，
// 会话详情里仍可手动收录。
type artifactExtractor struct {
	st       *store.Store
	attach   *attachmentstore.Store
	artifact svcartifact.ArtifactSvc
	material svcmaterial.MaterialSvc
	maxBytes int64
}

// newArtifactExtractor 构造抽取器。任一依赖为 nil（memory 模式）时返回 nil（Runner 侧 nil 安全）。
func newArtifactExtractor(st *store.Store, attach *attachmentstore.Store, art svcartifact.ArtifactSvc, mat svcmaterial.MaterialSvc) session.ArtifactExtractor {
	if st == nil || attach == nil || art == nil {
		return nil
	}
	return &artifactExtractor{st: st, attach: attach, artifact: art, material: mat, maxBytes: defaultMaxSnapshotBytes}
}

// ExtractToolWrite 见接口注释。任何失败仅 WARN（C4）。
//
// **只处理 present**：present 一次可声明多文件（office 等经 Python exec 产出的成品靠它显式声明）。
// write/edit 写出的文件是「产出（produced）」而非「交付物」，dsh 自身也只把它用于正文行内引用，
// 不当作产物——平台同样不再登记（否则中间脚本会污染「本轮产物」与资料库）。
func (x *artifactExtractor) ExtractToolWrite(ctx context.Context, sessionID, runID string, info protocol.ToolCallInfo) {
	rp, ok := workartifact.ResolveToolPresent(info)
	if !ok {
		// 不是 present：write/edit 等一律不登记（产出 ≠ 交付物）。
		return
	}
	sess, ok, err := x.st.GetSession(ctx, sessionID)
	if err != nil || !ok {
		glog.Warnw(ctx, "产物抽取（present）：会话不存在，跳过", "session", sessionID, "err", err)
		return
	}
	for _, p := range rp.Paths {
		// 每个文件独立捕获/落盘；单个失败不影响其余文件（WARN 由 captureOne 内部记）。
		x.captureOne(ctx, sess.Cwd, sessionID, runID, info.ID, p, rp.Origin)
	}
}

// captureOne 对一个「工具声明/写出的路径」做：解析 cwd 内绝对路径 → 越界校验 → 普通文件/阈值检查 →
// 同步读字节进内存 → 异步落盘登记。任何失败仅 WARN/跳过（C4），绝不 panic 上抛（外层另有 recover）。
//
// 单文件与 present 多文件共用这一份逻辑：present 对每个文件各调一次，互不影响。
func (x *artifactExtractor) captureOne(ctx context.Context, cwd, sessionID, runID, toolCallID, rawPath string, origin artifact.Origin) {
	// ② 解析 cwd（会话工作目录）→ 绝对路径 + 越界校验。cwd 拿不到/路径越界 → WARN 跳过。
	abs, rel, err := workartifact.SafeRelPath(cwd, rawPath)
	if err != nil {
		glog.Warnw(ctx, "产物抽取：路径解析失败，跳过", "session", sessionID, "path", rawPath, "err", err)
		return
	}
	// ③ 只认普通文件（不抽目录/符号链接）。
	fi, err := os.Stat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		// 文件可能刚被删/是目录：不是可抽产物，静默（WARN 太吵，debug 级）。
		glog.Debugw(ctx, "产物抽取：非普通文件，跳过", "path", abs)
		return
	}
	// ④ 阈值（R2 已拍板：超阈值**跳过不登记** + WARN，不造 upload_id 空的行）。
	if fi.Size() > x.maxBytes {
		glog.Warnw(ctx, "产物抽取：文件超快照上限，跳过", "path", rel, "size", fi.Size(), "max", x.maxBytes)
		return
	}
	// ⑤ 同步捕获：把字节读进内存 buffer（限读 maxBytes 兜底，防读取期间文件被写大）。
	buf, err := readFileCapped(abs, x.maxBytes)
	if err != nil {
		glog.Warnw(ctx, "产物抽取：读文件失败，跳过", "path", rel, "err", err)
		return
	}
	// ⑥ 异步落盘：算哈希 → filestore → artifacts 行。此后与工作区/dsh 完全解耦。
	title := filepath.Base(abs)
	mime := "" // 不探测类型（产物表不强猜 mime；如需可按扩展名补，后续再评估）
	go x.persist(sessionID, runID, toolCallID, title, rel, mime, string(origin), buf)
}

// persist 异步段：算 sha256 → 上传 filestore → 建 artifacts 行。失败仅 WARN（C4）。
func (x *artifactExtractor) persist(sessionID, runID, toolCallID, title, path, mime, origin string, buf []byte) {
	defer func() {
		if rec := recover(); rec != nil {
			glog.Warnw(context.Background(), "产物抽取落盘 panic（已吞）",
				"session", sessionID, "tool_call", toolCallID, "recover", rec)
		}
	}()
	ctx := context.Background()
	sum := sha256.Sum256(buf)
	hash := hex.EncodeToString(sum[:])
	detail, err := x.attach.UploadAndRecord(ctx, attachmentstore.UploadAndRecordRequest{
		ContentHash: hash,
		Name:        title,
		Size:        int64(len(buf)),
		MimeType:    mime,
		Reader:      bytes.NewReader(buf),
		Scene:       "artifact", // filestore 侧弱分组标记
	})
	if err != nil {
		glog.Warnw(ctx, "产物抽取：上传 filestore 失败，跳过", "session", sessionID, "tool_call", toolCallID, "err", err)
		return
	}
	resp, err := x.artifact.Register(ctx, dtoartifact.RegisterReq{
		FileID:     detail.FileUploadID,
		SessionID:  sessionID,
		RunID:      runID,
		ToolCallID: toolCallID,
		Origin:     origin,
		Kind:       string(artifact.KindFile),
		Title:      title,
		Path:       path,
		MimeType:   mime,
		SizeBytes:  int64(len(buf)),
	})
	if err != nil {
		glog.Warnw(ctx, "产物抽取：登记产物失败，跳过", "session", sessionID, "tool_call", toolCallID, "err", err)
		return
	}
	// 自动收录进资料库（幂等：同一 artifact 重复收录返回既有节点）。
	//
	// 失败了**不回滚产物行**：artifact 是溯源事实，资料库只是它的一个组织视图；用户仍可在
	// 会话详情「产物」区手动收录（那条路会回显真实错误）。
	if x.material == nil {
		return
	}
	if _, err := x.material.ImportArtifact(ctx, dtomaterial.ImportArtifactReq{ArtifactID: resp.ID}); err != nil {
		glog.Warnw(ctx, "产物抽取：自动收录进资料库失败（产物行已登记，可手动收录）",
			"session", sessionID, "artifact", resp.ID, "title", title, "err", err)
	}
}

// readFileCapped 读文件内容，最多 max 字节（超出 → 视为超阈值错误，由调用方按 R2 跳过）。
// 用 LimitReader 多读 1 字节探测「是否超限」，避免把 32 MiB 整段读进内存才发现超了。
func readFileCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path) // 只读打开（零侵入 C2：只读文件）
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(buf)) > max {
		return nil, errTooLarge
	}
	return buf, nil
}

// errTooLarge 文件在读取窗口内超过上限（极少数：Stat 与 Read 之间被写大）。
var errTooLarge = &snapshotTooLargeError{}

type snapshotTooLargeError struct{}

func (*snapshotTooLargeError) Error() string { return "artifact: snapshot exceeds max bytes" }

// artifactDirtyChecker 实现 svcartifact.DirtyChecker：用 sessions.cwd + artifact.Path 定位工作区
// 原文件，重算 sha256 与生成时快照（content_sha256）比对（R5/R6 已拍板：仅详情算、回写）。
type artifactDirtyChecker struct {
	st *store.Store
}

// CheckDirty 见接口注释。判不出（无 path / 无会话 cwd）→ (false,false)；文件已删 → (true,true)。
func (d *artifactDirtyChecker) CheckDirty(ctx context.Context, a model.ArtifactEntity) (bool, bool) {
	if a.Path == "" || d.st == nil {
		return false, false // 无工作区原文件（显式上传/内联产物）→ 不可判定
	}
	sess, ok, err := d.st.GetSession(ctx, a.SessionID)
	if err != nil || !ok || sess.Cwd == "" {
		return false, false
	}
	abs, _, err := workartifact.SafeRelPath(sess.Cwd, a.Path)
	if err != nil {
		return false, false // 路径解析失败 → 不可判定（不臆造 dirty）
	}
	fi, err := os.Stat(abs)
	if err != nil || !fi.Mode().IsRegular() {
		return true, true // 工作区原文件已删/不是普通文件 → 视为 dirty（R5）
	}
	// 重算工作区当前哈希（不受快照上限约束：只读、只算，不落盘；读失败 → 不可判定）。
	f, err := os.Open(abs)
	if err != nil {
		return false, false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, false
	}
	return hex.EncodeToString(h.Sum(nil)) != a.ContentSha256, true
}

// 断言：*artifactDirtyChecker 满足 svcartifact.DirtyChecker。
var _ svcartifact.DirtyChecker = (*artifactDirtyChecker)(nil)
