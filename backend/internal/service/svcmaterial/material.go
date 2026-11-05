// Package svcmaterial 承载资料库（Material Library）面的用例：目录树节点的建/列/详情/改名/移动/删、
// 文件上传建节点、关键词检索。
//
// 两条硬边界（设计 .dsh/docs/specs/2026-11-03-material-library-design.md）：
//   - 字节落盘/去重**复用附件链路**（svcattachment 的 Upload + attachmentstore），本包不碰磁盘布局；
//   - 业务数据（节点树/关联/表格）经 NodeStore 窄端口读写，本包不 import persistence/store·dao
//     （层表：service L4 不得 import persistence/store·dao）。
//
// 本期范围：只做资料库、不做知识库——「找」是**关键词子串匹配**，不含语义检索/向量/问答。
package svcmaterial

import (
	"context"
	"io"
	"strings"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/core/object/objid"
	"github.com/morehao/ark-mate/internal/core/object/objtime"

	"github.com/morehao/ark-mate/internal/domain/material"
	"github.com/morehao/ark-mate/internal/dto/dtomaterial"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// NodeStore 资料库持久层的**窄端口**（由 *store.Store 结构上满足，装配根交下来时现判 nil）。
//
// 只暴露本用例真正用到的读；写走各原子的 Insert/Update/Delete，保证「节点 + 关联 + 表格」
// 在一个受理点内一致（事务语义由 store 方法各自持有，跨表一致性由用例层编排）。
type NodeStore interface {
	InsertLibraryNode(ctx context.Context, n model.LibraryNodeEntity) error
	LibraryNodeByID(ctx context.Context, id string) (model.LibraryNodeEntity, bool, error)
	ListLibraryChildren(ctx context.Context, spaceID, parentID string) ([]model.LibraryNodeEntity, error)
	// ListLibraryChildrenByCategory 按分类过滤的一层子节点（category 空 = 不过滤）。
	ListLibraryChildrenByCategory(ctx context.Context, spaceID, parentID, category string) ([]model.LibraryNodeEntity, error)
	SearchLibraryNodes(ctx context.Context, spaceID, q string) ([]model.LibraryNodeEntity, error)
	// LibraryNodeByProvenance 按 provenance_id 找已收录的节点（幂等收录用；无命中 → ok=false）。
	LibraryNodeByProvenance(ctx context.Context, provenanceID string) (model.LibraryNodeEntity, bool, error)
	UpdateLibraryNode(ctx context.Context, id string, title, parentID, body *string) error
	// UpdateLibraryArtifactNode 把产物节点的载体换成同一文件的最新快照（自动收录去重用）。
	UpdateLibraryArtifactNode(ctx context.Context, nodeID, provenanceID, driveUploadID string, sizeBytes int64) error
	DeleteLibraryNode(ctx context.Context, id string) error

	InsertLibraryAttachment(ctx context.Context, a model.LibraryAttachmentEntity) error
	LibraryAttachmentsByNode(ctx context.Context, nodeID string) ([]model.LibraryAttachmentEntity, error)
	DeleteLibraryAttachmentsByNode(ctx context.Context, nodeID string) error

	UpsertLibraryTable(ctx context.Context, t model.LibraryTableEntity) error
	LibraryTableByNode(ctx context.Context, nodeID string) (model.LibraryTableEntity, bool, error)
	DeleteLibraryTable(ctx context.Context, nodeID string) error
}

// AttachmentUploader 复用附件链路的上传（svcattachment.AttachmentSvc 结构上满足）。
//
// 一次 upload 落一个文件 → 返回平台附件 id（att_…）；本用例再据扩展名建节点 + 关联。
type AttachmentUploader interface {
	Upload(ctx context.Context, fileName, declaredName, declaredMime string, r io.Reader) (UploadedAttachment, error)
}

// UploadedAttachment 是 upload 返回值的最小投影（只取本域需要的字段）。
// 由装配根用 svcattachment.Upload 的返回值投影填充——用例层零互引，接线点负责适配。
type UploadedAttachment struct {
	ID        string
	Name      string
	MimeType  string
	SizeBytes int64
}

// PagePublisher 把 page 节点的 HTML 落盘成本机可打开的文件（实现里用 `<MATE_HOME>/material_pages`）。
//
// 用例层不 import infra/config：只认「给节点 id + HTML，还落盘绝对路径」。回环查看路径由控制器拼。
type PagePublisher interface {
	WritePage(ctx context.Context, nodeID, html string) (absPath string, err error)
}

// AttachmentReader 由**平台附件 id**（`att_…`）解析出本地只读绝对路径。
//
// 适配器内部做 att→upload_id（store.AttachmentByID）→ 磁盘路径（attachmentstore.ReadPath）两步；
// 用例层只认「给附件 id、还本地路径」这一个语义（不 import store/infra）。
type AttachmentReader interface {
	LocalPathByAttachmentID(ctx context.Context, attachmentID string) (string, error)
}

// MaterialSvc 资料库面用例。
type MaterialSvc interface {
	ListNodes(ctx context.Context, parentID string, limit, offset int) (*dtomaterial.ListNodesResp, error)
	// ListNodesByCategory 列目录并按分类过滤（category 空 = 全部分类）。v34 起用于「资料/产物」分栏。
	ListNodesByCategory(ctx context.Context, parentID, category string, limit, offset int) (*dtomaterial.ListNodesResp, error)
	GetNode(ctx context.Context, id string) (*dtomaterial.NodeDetailResp, error)
	CreateNode(ctx context.Context, req dtomaterial.CreateNodeReq) (*dtomaterial.NodeDetailResp, error)
	PatchNode(ctx context.Context, id string, req dtomaterial.PatchNodeReq) (*dtomaterial.NodeDetailResp, error)
	DeleteNode(ctx context.Context, id string) (*dtomaterial.DeleteNodeResp, error)
	Upload(ctx context.Context, fileName, declaredName, declaredMime string, parentID string, r io.Reader) (*dtomaterial.NodeDetailResp, error)
	Search(ctx context.Context, q string, limit, offset int) (*dtomaterial.SearchResp, error)
	// DrivePath 返回 drive 节点主附件的本地只读绝对路径（给 Agent 的 read 工具用）。
	DrivePath(ctx context.Context, nodeID string) (string, error)

	// GetTableRows 取 table 节点的 schema + 行（非 table → 40001）。
	GetTableRows(ctx context.Context, nodeID string) (*dtomaterial.RowsResp, error)
	// SetTableRows 整表替换行数据（首版单列 JSON 整体写；非 table → 40001）。
	SetTableRows(ctx context.Context, nodeID, rows string) (*dtomaterial.RowsResp, error)
	// PublishPage 把 page 节点 body 落盘（page 种类；非 page → 40001）。返回落盘绝对路径。
	PublishPage(ctx context.Context, nodeID string) (*dtomaterial.PublishPageResp, error)
	// PageHTML 取 page 节点 body（view 直接回这段 HTML）。
	PageHTML(ctx context.Context, nodeID string) (string, error)

	// ImportArtifact 把一个产物收录进资料库（artifact_id → 建 category='artifact' 的节点）。
	// 跨域编排只在用例层：读产物（经 ArtifactRef 窄端口）→ 按 kind 映射 node_kind → 建节点。
	ImportArtifact(ctx context.Context, req dtomaterial.ImportArtifactReq) (*dtomaterial.NodeDetailResp, error)
}

// ArtifactRef 产物信息的**窄端口**（由 svcartifact/装配根适配，本包不 import svcartifact）。
//
// 只暴露收录真正用到的字段；ArtifactID 对应 artifacts.id，UploadID 对应 core_file_upload.id。
type ArtifactRef struct {
	ID        string
	SessionID string
	RunID     string
	Kind      string // file|text|table|link
	Title     string
	MimeType  string
	SizeBytes int64
	UploadID  string
	// Path 工作区相对路径（仅工具写工作区文件的产物有；自动收录按它做「一文件一节点」去重）。
	Path        string
	ContentText string
}

// ArtifactReader 按 id 读产物（不存在 → 返回 ok=false）。由装配根用 svcartifact 适配。
type ArtifactReader interface {
	ArtifactRef(ctx context.Context, id string) (ArtifactRef, bool, error)
	// PriorArtifactIDs 返回同一会话、同一工作区路径下**更早**的产物 id（新→旧，不含 excludeID）。
	//
	// 自动收录用它做「一文件一节点」：同一文件被改过多次时，复用已有节点、把载体换成最新快照。
	PriorArtifactIDs(ctx context.Context, sessionID, path, excludeID string) ([]string, error)
}

type materialSvc struct {
	st     NodeStore
	attach AttachmentUploader
	blobs  AttachmentReader
	page   PagePublisher
	arts   ArtifactReader
}

// New 构造资料库用例。st / attach / blobs 由装配根交下来（memory 模式可为 nil → 40501）。
// arts 为产物读取窄端口（收录产物时用；memory 模式可 nil）。
func New(st NodeStore, attach AttachmentUploader, blobs AttachmentReader, page PagePublisher, arts ArtifactReader) MaterialSvc {
	return &materialSvc{st: st, attach: attach, blobs: blobs, page: page, arts: arts}
}

// 默认分页。
const (
	defaultLimit = 50
	maxLimit     = 200
	defaultSpace = model.LibrarySpaceDefault
)

func clampLimit(l int) int {
	if l <= 0 {
		return defaultLimit
	}
	if l > maxLimit {
		return maxLimit
	}
	return l
}

// requireStore 统一给出「无持久层」的 fail-closed（memory 模式）。
func (s *materialSvc) requireStore() error {
	if s.st == nil {
		return code.Errorf(code.NotSupported, "material_unavailable: 资料库需要持久层（当前为 memory 模式）")
	}
	return nil
}

// spaceOf 校验并返回空间 id：本期只支持默认个人空间（非默认 → 40501 space_not_supported）。
func spaceOf(spaceID string) (string, error) {
	if spaceID == "" || spaceID == defaultSpace {
		return defaultSpace, nil
	}
	return "", code.Errorf(code.NotSupported, "space_not_supported: 本期只支持默认个人空间，不支持空间 %q", spaceID)
}

// extKind 按文件扩展名映射节点种类（语义在服务端，前端不传 kind）。
//
// md→doc、csv→table、html/htm→page、其余→drive。空/未知扩展名 → drive。
func extKind(name string) material.NodeKind {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown"):
		return material.KindDoc
	case strings.HasSuffix(lower, ".csv"):
		return material.KindTable
	case strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, ".htm"):
		return material.KindPage
	default:
		return material.KindDrive
	}
}

// nodeToResp 把实体折成列表项。
func nodeToResp(n model.LibraryNodeEntity) dtomaterial.NodeResp {
	return dtomaterial.NodeResp{
		ID:           n.ID,
		ParentID:     n.ParentID,
		NodeKind:     n.NodeKind,
		Title:        n.Title,
		SizeBytes:    n.SizeBytes,
		Category:     n.Category,
		ProvenanceID: n.ProvenanceID,
		UpdatedAt:    objtime.Milli(n.UpdatedAt),
	}
}

// newID 铸一个资料库节点 id。
func newID() string { return objid.New(objid.LibraryNode) }
