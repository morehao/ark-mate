package svcmaterial

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/morehao/ark-mate/internal/core/code"
	"github.com/morehao/ark-mate/internal/domain/artifact"
	"github.com/morehao/ark-mate/internal/domain/material"
	"github.com/morehao/ark-mate/internal/dto/dtomaterial"
	"github.com/morehao/ark-mate/internal/persistence/model"
)

// snippetRadius 关键词命中的上下文半径（字符）。
const snippetRadius = 60

// ListNodes 列出某父节点下的一层子节点（分页）。
func (s *materialSvc) ListNodes(ctx context.Context, parentID string, limit, offset int) (*dtomaterial.ListNodesResp, error) {
	return s.listNodes(ctx, parentID, "", limit, offset)
}

// ListNodesByCategory 列目录并按分类过滤（category 空 = 全部分类）。v34 起用于「资料/产物」分栏。
func (s *materialSvc) ListNodesByCategory(ctx context.Context, parentID, category string, limit, offset int) (*dtomaterial.ListNodesResp, error) {
	if category != "" && category != model.LibraryNodeCategoryMaterial && category != model.LibraryNodeCategoryArtifact {
		return nil, code.Errorf(code.InvalidParam, "参数错误: 未知分类 %q（material|artifact）", category)
	}
	return s.listNodes(ctx, parentID, category, limit, offset)
}

// listNodes 列目录的共用实现（category 空 = 不过滤）。
func (s *materialSvc) listNodes(ctx context.Context, parentID, category string, limit, offset int) (*dtomaterial.ListNodesResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	limit = clampLimit(limit)
	if offset < 0 {
		offset = 0
	}
	var all []model.LibraryNodeEntity
	var err error
	if category == "" {
		all, err = s.st.ListLibraryChildren(ctx, defaultSpace, parentID)
	} else {
		all, err = s.st.ListLibraryChildrenByCategory(ctx, defaultSpace, parentID, category)
	}
	if err != nil {
		return nil, code.Errorf(code.Internal, "列出资料库目录失败: %v", err)
	}
	total := len(all)
	end := offset + limit
	if end > total {
		end = total
	}
	if offset > total {
		offset = total
	}
	page := all[offset:end]
	out := make([]dtomaterial.NodeResp, 0, len(page))
	for _, n := range page {
		out = append(out, nodeToResp(n))
	}
	return &dtomaterial.ListNodesResp{
		Nodes:   out,
		HasMore: end < total,
		Offset:  offset,
		Limit:   limit,
	}, nil
}

// GetNode 取节点详情（元数据 + 按 kind 裁剪的载体定位）。
func (s *materialSvc) GetNode(ctx context.Context, id string) (*dtomaterial.NodeDetailResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	n, ok, err := s.st.LibraryNodeByID(ctx, id)
	if err != nil {
		return nil, code.Errorf(code.Internal, "读取资料库节点失败: %v", err)
	}
	if !ok {
		return nil, code.Errorf(code.NotFound, "material_node_not_found: 资料库节点不存在")
	}
	return s.detail(ctx, n)
}

// detail 把实体折成详情响应（按 kind 补载体）。
func (s *materialSvc) detail(ctx context.Context, n model.LibraryNodeEntity) (*dtomaterial.NodeDetailResp, error) {
	resp := &dtomaterial.NodeDetailResp{
		ID:            n.ID,
		ParentID:      n.ParentID,
		NodeKind:      n.NodeKind,
		Title:         n.Title,
		Body:          n.Body,
		TableID:       n.TableID,
		DriveUploadID: n.DriveUploadID,
		SourceURL:     n.SourceURL,
		SizeBytes:     n.SizeBytes,
		Category:      n.Category,
		ProvenanceID:  n.ProvenanceID,
		CreatedAt:     n.CreatedAt.UTC().Format(model.LayoutTS),
		UpdatedAt:     n.UpdatedAt.UTC().Format(model.LayoutTS),
	}
	if material.NodeKind(n.NodeKind) == material.KindTable && s.st != nil {
		if t, ok, err := s.st.LibraryTableByNode(ctx, n.ID); err != nil {
			return nil, code.Errorf(code.Internal, "读取表格 schema 失败: %v", err)
		} else if ok {
			resp.Schema = t.SchemaJSON
			resp.Rows = t.RowsJSON
		}
	}
	return resp, nil
}

// CreateNode 建一个节点（folder/doc/table/link/page/drive）。
//
// drive 节点由「P0′ 收口后的两步式」建：先 `POST /files` 拿 file_id，再 `CreateNode{node_kind:drive,
// drive_upload_id:file_id}`。**不再有 `/materials/upload` 端点**（上传统一走 ginupload）。
func (s *materialSvc) CreateNode(ctx context.Context, req dtomaterial.CreateNodeReq) (*dtomaterial.NodeDetailResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	space, err := spaceOf(req.SpaceID)
	if err != nil {
		return nil, err
	}
	kind := material.NodeKind(req.NodeKind)
	if !material.IsValidNodeKind(kind) {
		return nil, code.Errorf(code.InvalidParam, "invalid_node_kind: 未知节点种类 %q", req.NodeKind)
	}
	if strings.TrimSpace(req.Title) == "" {
		return nil, code.Errorf(code.InvalidParam, "参数错误: title 不能为空")
	}
	// 载体一致性（domain 判决）：drive 要求 drive_upload_id 非空、body 空。
	carrier := material.Carrier{Body: req.Body, TableID: tableIDFor(kind, req.Schema), SourceURL: req.SourceURL, DriveUploadID: req.DriveUploadID}
	if err := material.ValidateCarrier(kind, carrier); err != nil {
		return nil, code.Errorf(code.InvalidRequest, "material_node_kind_mismatch: 节点种类与载体不匹配")
	}
	n := model.LibraryNodeEntity{
		ID:            newID(),
		SpaceID:       space,
		ParentID:      req.ParentID,
		NodeKind:      string(kind),
		Title:         strings.TrimSpace(req.Title),
		Body:          req.Body,
		SourceURL:     req.SourceURL,
		DriveUploadID: req.DriveUploadID,
		Category:      model.LibraryNodeCategoryMaterial, // 用户自建资料
	}
	if kind == material.KindTable {
		n.TableID = n.ID // table_id 自指到本节点（library_tables.node_id = 本节点 id）
	}
	if err := s.st.InsertLibraryNode(ctx, n); err != nil {
		return nil, code.Errorf(code.Internal, "建资料库节点失败: %v", err)
	}
	if kind == material.KindTable {
		schema := req.Schema
		if strings.TrimSpace(schema) == "" {
			schema = "{}"
		}
		if err := s.st.UpsertLibraryTable(ctx, model.LibraryTableEntity{
			NodeID: n.ID, SchemaJSON: schema, RowsJSON: "[]",
		}); err != nil {
			return nil, code.Errorf(code.Internal, "建表格失败: %v", err)
		}
	}
	// 回填持久化时间（插入时 store 补的 CreatedAt/UpdatedAt 只写进它的副本；重查拿权威值）。
	if saved, ok, err := s.st.LibraryNodeByID(ctx, n.ID); err == nil && ok {
		n = saved
	}
	return s.detail(ctx, n)
}

// tableIDFor table 种类的 TableID 取本节点 id（建时未知，此处仅用于载体校验占位）。
func tableIDFor(kind material.NodeKind, schema string) string {
	if kind == material.KindTable {
		return "pending" // 建节点后回填为自身 id；载体校验只判"非空"
	}
	return ""
}

// PatchNode 改名/移动/改正文。
func (s *materialSvc) PatchNode(ctx context.Context, id string, req dtomaterial.PatchNodeReq) (*dtomaterial.NodeDetailResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	n, ok, err := s.st.LibraryNodeByID(ctx, id)
	if err != nil {
		return nil, code.Errorf(code.Internal, "读取资料库节点失败: %v", err)
	}
	if !ok {
		return nil, code.Errorf(code.NotFound, "material_node_not_found: 资料库节点不存在")
	}
	// 移动：防环 + 目标父存在性。
	if req.ParentID != nil {
		newParent := *req.ParentID
		if newParent != "" {
			if _, ok, err := s.st.LibraryNodeByID(ctx, newParent); err != nil {
				return nil, code.Errorf(code.Internal, "读取目标父节点失败: %v", err)
			} else if !ok {
				return nil, code.Errorf(code.NotFound, "material_node_not_found: 目标父节点不存在")
			}
		}
		ancestors, err := s.ancestors(ctx, newParent)
		if err != nil {
			return nil, err
		}
		if material.WouldCycle(id, newParent, ancestors) {
			return nil, code.Errorf(code.InvalidRequest, "material_cycle: 不能把节点移动到它自己或它的子孙下")
		}
	}
	// 改正文：仅 doc/link/page。
	if req.Body != nil {
		if k := material.NodeKind(n.NodeKind); k != material.KindDoc && k != material.KindLink && k != material.KindPage {
			return nil, code.Errorf(code.InvalidRequest, "material_node_kind_mismatch: 该种类节点不支持改正文")
		}
	}
	if req.Title != nil && strings.TrimSpace(*req.Title) == "" {
		return nil, code.Errorf(code.InvalidParam, "参数错误: title 不能为空")
	}
	if err := s.st.UpdateLibraryNode(ctx, id, req.Title, req.ParentID, req.Body); err != nil {
		return nil, code.Errorf(code.Internal, "更新资料库节点失败: %v", err)
	}
	updated, _, _ := s.st.LibraryNodeByID(ctx, id)
	return s.detail(ctx, updated)
}

// ancestors 取某节点向上的祖先链（含自身），给 WouldCycle 用。
func (s *materialSvc) ancestors(ctx context.Context, id string) ([]string, error) {
	var chain []string
	cur := id
	for i := 0; i < 10000 && cur != ""; i++ { // 防环硬上限（坏库里的环也不会死循环）
		n, ok, err := s.st.LibraryNodeByID(ctx, cur)
		if err != nil {
			return nil, code.Errorf(code.Internal, "读取祖先链失败: %v", err)
		}
		if !ok {
			break
		}
		chain = append(chain, n.ID)
		cur = n.ParentID
	}
	return chain, nil
}

// DeleteNode 删节点（folder 级联删子树；表格行 + 关联一并删）。
//
// 物理字节（drive 的附件）由调用方按 filestore 去重规则处理——本期先不做 blob 回收（与附件
// 链路的孤儿清扫同一层，后续接入；此处只删平台行，不影响附件链路本身）。
func (s *materialSvc) DeleteNode(ctx context.Context, id string) (*dtomaterial.DeleteNodeResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	n, ok, err := s.st.LibraryNodeByID(ctx, id)
	if err != nil {
		return nil, code.Errorf(code.Internal, "读取资料库节点失败: %v", err)
	}
	if !ok {
		return &dtomaterial.DeleteNodeResp{Deleted: false}, nil // 幂等
	}
	if err := s.deleteSubtree(ctx, n); err != nil {
		return nil, err
	}
	return &dtomaterial.DeleteNodeResp{Deleted: true}, nil
}

// deleteSubtree 递归删一棵子树（先删子节点再删自身）。
func (s *materialSvc) deleteSubtree(ctx context.Context, n model.LibraryNodeEntity) error {
	children, err := s.st.ListLibraryChildren(ctx, n.SpaceID, n.ID)
	if err != nil {
		return code.Errorf(code.Internal, "读取子节点失败: %v", err)
	}
	for _, c := range children {
		if err := s.deleteSubtree(ctx, c); err != nil {
			return err
		}
	}
	if material.NodeKind(n.NodeKind) == material.KindTable {
		if err := s.st.DeleteLibraryTable(ctx, n.ID); err != nil {
			return code.Errorf(code.Internal, "删除表格失败: %v", err)
		}
	}
	if err := s.st.DeleteLibraryAttachmentsByNode(ctx, n.ID); err != nil {
		return code.Errorf(code.Internal, "删除节点附件关联失败: %v", err)
	}
	if err := s.st.DeleteLibraryNode(ctx, n.ID); err != nil {
		return code.Errorf(code.Internal, "删除资料库节点失败: %v", err)
	}
	return nil
}

// Upload 上传一个文件成节点（先走附件链路落字节拿 att_…，再按扩展名建节点 + 关联）。
func (s *materialSvc) Upload(ctx context.Context, fileName, declaredName, declaredMime string, parentID string, r io.Reader) (*dtomaterial.NodeDetailResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	if s.attach == nil {
		return nil, code.Errorf(code.NotSupported, "material_unavailable: 附件上传不可用（当前为 memory 模式）")
	}
	up, err := s.attach.Upload(ctx, fileName, declaredName, declaredMime, r)
	if err != nil {
		// 附件链路的错误已带码（attachment_too_large 等），原样上抛。
		return nil, err
	}
	// 本期（P1）上传一律落成 **drive 承载**：字节在附件链路（可下载），节点只做目录归属。
	// md/html/csv → doc/page/table 的内容解析不在 P1（无解析能力），留 P3；届时按扩展名改建类型。
	title := up.Name
	if strings.TrimSpace(title) == "" {
		title = fileName
	}
	n := model.LibraryNodeEntity{
		ID:            newID(),
		SpaceID:       defaultSpace,
		ParentID:      parentID,
		NodeKind:      string(material.KindDrive),
		Title:         title,
		DriveUploadID: up.ID,
		SizeBytes:     up.SizeBytes,
		Category:      model.LibraryNodeCategoryMaterial,
	}
	if err := s.st.InsertLibraryNode(ctx, n); err != nil {
		return nil, code.Errorf(code.Internal, "建资料库节点失败: %v", err)
	}
	if err := s.st.InsertLibraryAttachment(ctx, model.LibraryAttachmentEntity{
		NodeID:       n.ID,
		AttachmentID: up.ID,
		Role:         model.LibraryAttachmentRolePrimary,
	}); err != nil {
		return nil, code.Errorf(code.Internal, "登记节点附件关联失败: %v", err)
	}
	// 回填持久化时间（插入时 store 补的 CreatedAt/UpdatedAt 只写进它的副本；重查拿权威值）。
	if saved, ok, err := s.st.LibraryNodeByID(ctx, n.ID); err == nil && ok {
		n = saved
	}
	return s.detail(ctx, n)
}

// Search 关键词检索（title/body 子串，大小写不敏感）。**非语义检索**。
func (s *materialSvc) Search(ctx context.Context, q string, limit, offset int) (*dtomaterial.SearchResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, code.Errorf(code.InvalidParam, "参数错误: 关键词 q 不能为空")
	}
	if utf8.RuneCountInString(q) > 128 {
		return nil, code.Errorf(code.InvalidParam, "参数错误: 关键词过长（上限 128 字符）")
	}
	limit = clampLimit(limit)
	if offset < 0 {
		offset = 0
	}
	all, err := s.st.SearchLibraryNodes(ctx, defaultSpace, q)
	if err != nil {
		return nil, code.Errorf(code.Internal, "检索资料库失败: %v", err)
	}
	total := len(all)
	end := offset + limit
	if end > total {
		end = total
	}
	if offset > total {
		offset = total
	}
	hits := make([]dtomaterial.SearchHit, 0, end-offset)
	for _, n := range all[offset:end] {
		hits = append(hits, dtomaterial.SearchHit{
			NodeID:   n.ID,
			Title:    n.Title,
			NodeKind: n.NodeKind,
			Snippet:  snippet(n.Title, n.Body, q),
		})
	}
	resp := &dtomaterial.SearchResp{Hits: hits, Total: total, HasMore: end < total}
	if total == 0 {
		resp.Note = "没有找到匹配的资料，可换关键词或逐级浏览目录"
	}
	return resp, nil
}

// snippet 在 title/body 里定位关键词，截取前后各 snippetRadius 字符的纯文本片段。
func snippet(title, body, q string) string {
	src := body
	if src == "" {
		src = title
	}
	lowerSrc := strings.ToLower(src)
	idx := strings.Index(lowerSrc, strings.ToLower(q))
	runes := []rune(src)
	if idx < 0 {
		// body 里没命中（可能只命中 title）：回退给 body 开头一段。
		if len(runes) > snippetRadius*2 {
			return string(runes[:snippetRadius*2])
		}
		return src
	}
	// 把字节下标转成 rune 下标。
	runeIdx := utf8.RuneCountInString(src[:idx])
	start := runeIdx - snippetRadius
	if start < 0 {
		start = 0
	}
	endR := runeIdx + utf8.RuneCountInString(q) + snippetRadius
	if endR > len(runes) {
		endR = len(runes)
	}
	prefix := ""
	if start > 0 {
		prefix = "…"
	}
	suffix := ""
	if endR < len(runes) {
		suffix = "…"
	}
	return prefix + string(runes[start:endR]) + suffix
}

// DrivePath 返回 drive 节点主附件的本地只读绝对路径（给 Agent 的 read 工具用）。
//
// 一个 drive 节点恰有一条 role=primary 的附件关联（P1 Upload 建的就是它）；取其附件 id，
// 交给 AttachmentReader 解析成本地路径。非 drive 节点 / 无附件 → 40001。
func (s *materialSvc) DrivePath(ctx context.Context, nodeID string) (string, error) {
	if err := s.requireStore(); err != nil {
		return "", err
	}
	if s.blobs == nil {
		return "", code.Errorf(code.NotSupported, "material_unavailable: 附件读取不可用（当前为 memory 模式）")
	}
	n, ok, err := s.st.LibraryNodeByID(ctx, nodeID)
	if err != nil {
		return "", code.Errorf(code.Internal, "读取资料库节点失败: %v", err)
	}
	if !ok {
		return "", code.Errorf(code.NotFound, "material_node_not_found: 资料库节点不存在")
	}
	if material.NodeKind(n.NodeKind) != material.KindDrive {
		return "", code.Errorf(code.InvalidRequest, "material_node_kind_mismatch: 只有 drive 节点有可下载文件路径")
	}
	atts, err := s.st.LibraryAttachmentsByNode(ctx, nodeID)
	if err != nil {
		return "", code.Errorf(code.Internal, "读取节点附件关联失败: %v", err)
	}
	var primaryID string
	for _, a := range atts {
		if a.Role == model.LibraryAttachmentRolePrimary {
			primaryID = a.AttachmentID
			break
		}
	}
	if primaryID == "" {
		return "", code.Errorf(code.NotFound, "material_node_not_found: drive 节点没有主附件")
	}
	path, err := s.blobs.LocalPathByAttachmentID(ctx, primaryID)
	if err != nil {
		return "", code.Errorf(code.Internal, "解析附件本地路径失败: %v", err)
	}
	return path, nil
}

// ——— P3：table 行 CRUD + page 发布/查看 ———

// tableNode 取一个节点并校验它是 table 种类；返回其表格行。
func (s *materialSvc) tableNode(ctx context.Context, nodeID string) (model.LibraryNodeEntity, model.LibraryTableEntity, error) {
	n, ok, err := s.st.LibraryNodeByID(ctx, nodeID)
	if err != nil {
		return model.LibraryNodeEntity{}, model.LibraryTableEntity{}, code.Errorf(code.Internal, "读取资料库节点失败: %v", err)
	}
	if !ok {
		return model.LibraryNodeEntity{}, model.LibraryTableEntity{}, code.Errorf(code.NotFound, "material_node_not_found: 资料库节点不存在")
	}
	if material.NodeKind(n.NodeKind) != material.KindTable {
		return model.LibraryNodeEntity{}, model.LibraryTableEntity{}, code.Errorf(code.InvalidRequest, "material_node_kind_mismatch: 只有 table 节点有表格数据")
	}
	t, ok, err := s.st.LibraryTableByNode(ctx, nodeID)
	if err != nil {
		return model.LibraryNodeEntity{}, model.LibraryTableEntity{}, code.Errorf(code.Internal, "读取表格失败: %v", err)
	}
	if !ok {
		// 兼容坏数据：table 节点却无表格行 → 视为空表。
		t = model.LibraryTableEntity{NodeID: nodeID, SchemaJSON: "{}", RowsJSON: "[]"}
	}
	return n, t, nil
}

// GetTableRows 取 table 节点的 schema + 行。
func (s *materialSvc) GetTableRows(ctx context.Context, nodeID string) (*dtomaterial.RowsResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	_, t, err := s.tableNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	return &dtomaterial.RowsResp{NodeID: nodeID, Schema: t.SchemaJSON, Rows: t.RowsJSON}, nil
}

// SetTableRows 整表替换行数据（首版单列 JSON 整体写；rows 必须是 JSON 数组）。
func (s *materialSvc) SetTableRows(ctx context.Context, nodeID, rows string) (*dtomaterial.RowsResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	_, t, err := s.tableNode(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	rows = strings.TrimSpace(rows)
	if rows == "" {
		rows = "[]"
	}
	if !json.Valid([]byte(rows)) || !strings.HasPrefix(rows, "[") {
		return nil, code.Errorf(code.InvalidParam, "参数错误: rows 必须是 JSON 数组字符串")
	}
	if err := s.st.UpsertLibraryTable(ctx, model.LibraryTableEntity{
		NodeID: nodeID, SchemaJSON: t.SchemaJSON, RowsJSON: rows,
	}); err != nil {
		return nil, code.Errorf(code.Internal, "写表格行失败: %v", err)
	}
	// 表格内容变化也更新节点 updated_at（列表按它排序）。
	_ = s.st.UpdateLibraryNode(ctx, nodeID, nil, nil, nil)
	return &dtomaterial.RowsResp{NodeID: nodeID, Schema: t.SchemaJSON, Rows: rows}, nil
}

// PublishPage 把 page 节点 body 落盘（page 种类）。
func (s *materialSvc) PublishPage(ctx context.Context, nodeID string) (*dtomaterial.PublishPageResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	n, ok, err := s.st.LibraryNodeByID(ctx, nodeID)
	if err != nil {
		return nil, code.Errorf(code.Internal, "读取资料库节点失败: %v", err)
	}
	if !ok {
		return nil, code.Errorf(code.NotFound, "material_node_not_found: 资料库节点不存在")
	}
	if material.NodeKind(n.NodeKind) != material.KindPage {
		return nil, code.Errorf(code.InvalidRequest, "material_node_kind_mismatch: 只有 page 节点可发布")
	}
	if s.page == nil {
		return nil, code.Errorf(code.NotSupported, "material_unavailable: 页面发布不可用（当前为 memory 模式）")
	}
	html := n.Body
	if strings.TrimSpace(html) == "" {
		return nil, code.Errorf(code.InvalidParam, "参数错误: page 节点 body 为空，无可发布内容")
	}
	absPath, err := s.page.WritePage(ctx, nodeID, html)
	if err != nil {
		return nil, code.Errorf(code.Internal, "页面落盘失败: %v", err)
	}
	return &dtomaterial.PublishPageResp{
		NodeID:   nodeID,
		ViewPath: "/materials/nodes/" + nodeID + "/view",
		FilePath: absPath,
	}, nil
}

// PageHTML 取 page 节点 body（view 直接回这段 HTML）。
func (s *materialSvc) PageHTML(ctx context.Context, nodeID string) (string, error) {
	if err := s.requireStore(); err != nil {
		return "", err
	}
	n, ok, err := s.st.LibraryNodeByID(ctx, nodeID)
	if err != nil {
		return "", code.Errorf(code.Internal, "读取资料库节点失败: %v", err)
	}
	if !ok {
		return "", code.Errorf(code.NotFound, "material_node_not_found: 资料库节点不存在")
	}
	if material.NodeKind(n.NodeKind) != material.KindPage {
		return "", code.Errorf(code.InvalidRequest, "material_node_kind_mismatch: 只有 page 节点可查看")
	}
	return n.Body, nil
}

// ——— P1：产物收录进资料库 ———

// ImportArtifact 把一个产物收录进资料库（artifact_id → 建 category='artifact' 的节点）。
//
// 跨域编排只在用例层：读产物（经 ArtifactRef 窄端口）→ 按 kind 映射 node_kind → 建节点。
// 幂等：同一 provenance_id 已收录 → 返回既有节点（不重复建）。
// 不变量（A-3）：收录出的节点恒为 category='artifact' 且 provenance_id 非空（同真同假）。
func (s *materialSvc) ImportArtifact(ctx context.Context, req dtomaterial.ImportArtifactReq) (*dtomaterial.NodeDetailResp, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.ArtifactID) == "" {
		return nil, code.Errorf(code.InvalidParam, "参数错误: artifact_id 不能为空")
	}
	if s.arts == nil {
		return nil, code.Errorf(code.NotSupported, "artifact_unavailable: 产物收录需要持久层（当前为 memory 模式）")
	}
	ref, ok, err := s.arts.ArtifactRef(ctx, req.ArtifactID)
	if err != nil {
		return nil, code.Errorf(code.Internal, "读取产物失败: %v", err)
	}
	if !ok {
		return nil, code.Errorf(code.NotFound, "artifact_not_found: 产物不存在")
	}
	// 幂等：同一 provenance_id 已收录 → 返回既有节点。
	if existing, ok, err := s.nodeByProvenance(ctx, req.ArtifactID); err != nil {
		return nil, err
	} else if ok {
		return s.detail(ctx, existing)
	}
	// 按产物 kind 映射 node_kind（file→drive、text→doc、table→table、link→link）。
	nk, body, err := artifactKindToNode(ref)
	if err != nil {
		return nil, err
	}
	// 父节点存在性（若给了 parent）。
	if req.ParentID != "" {
		if _, ok, err := s.st.LibraryNodeByID(ctx, req.ParentID); err != nil {
			return nil, code.Errorf(code.Internal, "读取父节点失败: %v", err)
		} else if !ok {
			return nil, code.Errorf(code.NotFound, "material_node_not_found: 目标父节点不存在")
		}
	}
	n := model.LibraryNodeEntity{
		ID:           newID(),
		SpaceID:      defaultSpace,
		ParentID:     req.ParentID,
		NodeKind:     string(nk),
		Title:        ref.Title,
		Body:         body,
		SizeBytes:    ref.SizeBytes,
		Category:     model.LibraryNodeCategoryArtifact,
		ProvenanceID: ref.ID,
	}
	if nk == material.KindDrive {
		n.DriveUploadID = ref.UploadID
	}
	if err := s.st.InsertLibraryNode(ctx, n); err != nil {
		return nil, code.Errorf(code.Internal, "收录产物失败: %v", err)
	}
	if nk == material.KindTable {
		if err := s.st.UpsertLibraryTable(ctx, model.LibraryTableEntity{
			NodeID: n.ID, SchemaJSON: "{}", RowsJSON: defaultRows(ref.ContentText),
		}); err != nil {
			return nil, code.Errorf(code.Internal, "建表格失败: %v", err)
		}
	}
	// 回填持久化后的行（插入时 store 补的时间只写进它的副本；重查拿权威 created_at/updated_at）。
	if saved, ok, err := s.st.LibraryNodeByID(ctx, n.ID); err == nil && ok {
		n = saved
	}
	return s.detail(ctx, n)
}

// nodeByProvenance 按 provenance_id 找已收录节点（store 未提供按 provenance 查的端口时，走全列表扫）。
func (s *materialSvc) nodeByProvenance(ctx context.Context, artifactID string) (model.LibraryNodeEntity, bool, error) {
	// 本期单机规模：从空间根逐层找代价高，改用列出全部节点里的 artifact 分类（含子层）。
	// 简化：store 提供 SearchLibraryNodes 不覆盖；这里用 ListLibraryChildren 递归成本高。
	// 因此直接在 store 加一个按 provenance 的查询更稳——见 store.LibraryNodeByProvenance。
	return s.st.LibraryNodeByProvenance(ctx, artifactID)
}

// artifactKindToNode 把产物映射成 node_kind + 正文（file→drive 无正文；text→doc；link→link；table→table）。
func artifactKindToNode(ref ArtifactRef) (material.NodeKind, string, error) {
	switch artifact.Kind(ref.Kind) {
	case artifact.KindFile:
		return material.KindDrive, "", nil
	case artifact.KindText:
		return material.KindDoc, ref.ContentText, nil
	case artifact.KindLink:
		return material.KindLink, ref.ContentText, nil
	case artifact.KindTable:
		return material.KindTable, "", nil
	default:
		return "", "", code.Errorf(code.InvalidRequest, "artifact_kind_unsupported: 产物种类 %q 无法收录", ref.Kind)
	}
}

// defaultRows 表格产物的初始行（content_text 是 JSON 数组；空则 []）。
func defaultRows(contentText string) string {
	if strings.TrimSpace(contentText) == "" {
		return "[]"
	}
	return contentText
}
