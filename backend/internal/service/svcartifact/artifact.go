// Package svcartifact 承载产物面用例：登记（从已有 file_id 或不内联内容建产物行）、列表、详情。
//
// 两条硬边界（设计 §5.4）：
//   - **只建索引**：不接收 multipart、不算哈希、不落盘——字节统一在 `POST /files`（ginupload）已落，
//     本包只把「已有句柄」登记成产物行（承接 P0′ 上传收口）；
//   - 业务数据经 ArtifactStore 窄端口读写，本包不 import persistence/store·dao（层表：service L4
//     不得 import persistence/store·dao）；file 类产物的 sha256/size 经 FileMeta 端口从 filestore 取。
//
// **不 import domain/material**（依赖单向，A-1）：产物域不认识资料库域；跨域编排在 svcmaterial 侧。
package svcartifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/core/object/objtime"

	"github.com/morehao/ark-mate/internal/domain/artifact"
	"github.com/morehao/ark-mate/internal/dto/dtoartifact"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// ArtifactStore 产物持久层的**窄端口**（由 *store.Store 结构上满足）。
type ArtifactStore interface {
	InsertArtifact(ctx context.Context, a model.ArtifactEntity) error
	ArtifactByID(ctx context.Context, id string) (model.ArtifactEntity, bool, error)
	ListArtifactsBySession(ctx context.Context, sessionID string) ([]model.ArtifactEntity, error)
	UpdateArtifactDirty(ctx context.Context, id string, dirty int) error
}

// FileMeta 从 filestore 取 file 类产物的元数据（sha256 / size / name），用于登记时填充。
// 由装配根用 attachmentstore 适配（本包不 import infra）。
type FileMeta interface {
	// GetFileMeta 按 file_id 取 (contentHash, size, name)；不存在 → error。
	GetFileMeta(ctx context.Context, fileID string) (contentHash string, size int64, name string, err error)
}

// DirtyChecker 惰性判定产物「工作区当前文件是否已与生成时快照不一致」（dirty 语义，R5/R6 已拍板）。
//
// 本包不认识会话 cwd（依赖单向），「工作区原文件在哪、怎么算」由装配层实现（用 sessions.cwd +
// artifact.Path 定位并重算哈希）。读面**只在详情 `Get` 调用**（列表页不算，R6），判完回写 dirty。
type DirtyChecker interface {
	// CheckDirty 判定 a 当前是否 dirty。checked=false 表示无法判定（如 a.Path 为空、无 cwd），
	// 此时读面按**不可判定**处理（不强行改 dirty 存量）。工作区文件已被删 → (true, true)。
	CheckDirty(ctx context.Context, a model.ArtifactEntity) (dirty bool, checked bool)
}

// ArtifactSvc 产物面用例。
type ArtifactSvc interface {
	// Register 登记一条产物（file 类传 FileID；内联类传 ContentText）。
	Register(ctx context.Context, req dtoartifact.RegisterReq) (*dtoartifact.ArtifactResp, error)
	// ListBySession 列某会话的全部产物。
	ListBySession(ctx context.Context, sessionID string) (*dtoartifact.ListResp, error)
	// Get 取产物详情。
	Get(ctx context.Context, id string) (*dtoartifact.ArtifactResp, error)
}

type artifactSvc struct {
	st    ArtifactStore
	files FileMeta
	dirty DirtyChecker
}

// New 构造产物用例。files 可为 nil（内联产物仍可用；file 类产物登记时 file 元数据不可取 → 报错）。
func New(st ArtifactStore, files FileMeta, dirty DirtyChecker) ArtifactSvc {
	return &artifactSvc{st: st, files: files, dirty: dirty}
}

func (s *artifactSvc) requireStore() error {
	if s.st == nil {
		return code.Errorf(code.NotSupported, "artifact_unavailable: 产物需要持久层（当前为 memory 模式）")
	}
	return nil
}

// Register 登记一条产物（承接 P0′：字节已在 POST /files 落 filestore，本方法只建索引）。
func (s *artifactSvc) Register(ctx context.Context, req dtoartifact.RegisterReq) (*dtoartifact.ArtifactResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	kind := artifact.Kind(req.Kind)
	if !artifact.IsValidKind(kind) {
		return nil, code.Errorf(code.InvalidParam, "invalid_artifact_kind: 未知产物种类 %q", req.Kind)
	}
	origin := artifact.Origin(req.Origin)
	if !artifact.IsValidOrigin(origin) {
		return nil, code.Errorf(code.InvalidParam, "invalid_artifact_origin: 未知产物来源 %q", req.Origin)
	}
	if req.SessionID == "" {
		return nil, code.Errorf(code.InvalidParam, "参数错误: session_id 不能为空")
	}
	if req.Title == "" {
		req.Title = "未命名产物"
	}
	// 载体一致性（domain 判决）。
	carrier := artifact.Carrier{UploadID: req.FileID, ContentText: req.ContentText}
	if err := artifact.ValidateCarrier(kind, carrier); err != nil {
		return nil, code.Errorf(code.InvalidRequest, "artifact_kind_unsupported: 产物种类与载体不匹配")
	}

	n := model.ArtifactEntity{
		ID:          objid.New(objid.Artifact),
		SessionID:   req.SessionID,
		RunID:       req.RunID,
		ToolCallID:  req.ToolCallID,
		Origin:      string(origin),
		Kind:        string(kind),
		Title:       req.Title,
		Path:        req.Path,
		MimeType:    req.MimeType,
		SizeBytes:   req.SizeBytes,
		UploadID:    req.FileID,
		ContentText: req.ContentText,
	}
	// file 类：从 filestore 补 sha256/size/name（快照哈希是「生成时内容是什么」的锚点）。
	if kind == artifact.KindFile {
		if s.files == nil {
			return nil, code.Errorf(code.NotSupported, "artifact_unavailable: 文件产物需要字节存储（memory 模式不支持）")
		}
		hash, size, name, err := s.files.GetFileMeta(ctx, req.FileID)
		if err != nil {
			return nil, code.Errorf(code.NotFound, "artifact_file_not_found: file_id %s 不存在或不可读: %v", req.FileID, err)
		}
		n.ContentSha256 = hash
		if n.SizeBytes == 0 {
			n.SizeBytes = size
		}
		if n.MimeType == "" {
			n.MimeType = "" // mime 留由调用方给；此处不强猜（产物表不探测类型）
		}
		_ = name
	} else {
		// 内联类：快照哈希取 content_text 的摘要（由 store 层补？不，简单留给空；dirty 判定用得到）。
		n.ContentSha256 = contentDigest(req.ContentText)
	}

	if err := s.st.InsertArtifact(ctx, n); err != nil {
		return nil, code.Errorf(code.Internal, "登记产物失败: %v", err)
	}
	// 回填持久化后的行（store 在插入时补 CreatedAt/UpdatedAt 到自己的副本，调用方这份是值拷贝、
	// 时间仍零值）；重查一次拿到权威时间，避免「刚创建得到的 created_at 为空」。
	if saved, ok, err := s.st.ArtifactByID(ctx, n.ID); err == nil && ok {
		n = saved
	}
	return toResp(n), nil
}

// ListBySession 列某会话的全部产物。
func (s *artifactSvc) ListBySession(ctx context.Context, sessionID string) (*dtoartifact.ListResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	rows, err := s.st.ListArtifactsBySession(ctx, sessionID)
	if err != nil {
		return nil, code.Errorf(code.Internal, "列产物失败: %v", err)
	}
	out := make([]dtoartifact.ArtifactResp, 0, len(rows))
	for _, r := range rows {
		out = append(out, *toResp(r))
	}
	return &dtoartifact.ListResp{Artifacts: out, Total: len(out)}, nil
}

// Get 取产物详情。
func (s *artifactSvc) Get(ctx context.Context, id string) (*dtoartifact.ArtifactResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	a, ok, err := s.st.ArtifactByID(ctx, id)
	if err != nil {
		return nil, code.Errorf(code.Internal, "读取产物失败: %v", err)
	}
	if !ok {
		return nil, code.Errorf(code.NotFound, "artifact_not_found: 产物不存在")
	}
	// dirty 惰性判定（R5/R6）：**仅详情**做，列表页不算。判得出 → 回写 + 反映到响应；
	// 判不出（无工作区原文件/无 cwd）→ 保持存量 dirty（不臆造）。回写/判定失败都不影响读面。
	if s.dirty != nil {
		if dirty, checked := s.dirty.CheckDirty(ctx, a); checked {
			if (dirty && a.Dirty == 0) || (!dirty && a.Dirty != 0) {
				a.Dirty = 0
				if dirty {
					a.Dirty = 1
				}
				_ = s.st.UpdateArtifactDirty(ctx, a.ID, a.Dirty) // 回写 best-effort，失败不影响读
			}
		}
	}
	return toResp(a), nil
}

// toResp 实体 → 对外响应。
func toResp(a model.ArtifactEntity) *dtoartifact.ArtifactResp {
	return &dtoartifact.ArtifactResp{
		ID:            a.ID,
		SessionID:     a.SessionID,
		RunID:         a.RunID,
		ToolCallID:    a.ToolCallID,
		Origin:        a.Origin,
		Kind:          a.Kind,
		Title:         a.Title,
		Path:          a.Path,
		MimeType:      a.MimeType,
		SizeBytes:     a.SizeBytes,
		UploadID:      a.UploadID,
		ContentText:   a.ContentText,
		ContentSha256: a.ContentSha256,
		Dirty:         a.Dirty != 0,
		CreatedAt:     objtime.Milli(a.CreatedAt),
		UpdatedAt:     objtime.Milli(a.UpdatedAt),
	}
}

// contentDigest 内联内容的内容摘要（sha256 hex），作 content_sha256 快照锚点（file 类用 filestore 的 hash）。
func contentDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
