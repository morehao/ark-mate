// Package materialmcp 把「资料库」暴露成一条 **MCP 工具面**，交给底座（dsh）经 ACP 的
// `mcpServers` 拉起（`mate mcp material` 子命令，见 internal/bootstrap/maintenance/material_mcp.go）。
//
// 本期范围：只做资料库、不做知识库——因此**只有文件级工具**（列目录 / 节点信息 / 上传），
// **没有 library_search**（无语义检索，关键词检索也只走 REST，不进工具面）。
//
// 三条边界（设计 .dsh/docs/specs/2026-11-03-material-library-design.md §5.4）：
//   - 工具是**文件级**的：读正文交给底座原生 `read`，本包只回本地只读绝对路径（handle 模式），
//     **不写用户工作区、不内联 base64**；
//   - 用例层零改：本包只调 `svcmaterial.MaterialSvc`（ListNodes/GetNode/Upload/DrivePath），
//     不 import store/dao；
//   - `library_upload` 的 `path` 只接受**工作区或用户可信目录内的绝对路径**（与附件链路同纪律）。
package materialmcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/morehao/ark-mate/internal/dto/dtomaterial"

	"github.com/morehao/ark-mate/internal/core/protocol"
	"github.com/morehao/ark-mate/internal/infra/protocol/mcp"
)

// 工具名（固定；G5 契约快照钉住）。
const (
	ToolListNodes = "library_list_nodes"
	ToolNodeInfo  = "library_node_info"
	ToolUpload    = "library_upload"
)

// Service 是资料库工具面需要的**窄端口**（本包自声明，由装配根用 svcmaterial 实现）。
//
// 为什么不一上来就 import svcmaterial：层规只有装配根与控制器可以引用用例层；infra 这一层若
// 直接 import 用例层，就把"用例可以随便改"这条纪律的收益吃掉了。这里只声明工具面真正用到的
// 四个读/写方法（用 dto 值，不 import 用例层）。
type Service interface {
	ListNodes(ctx context.Context, parentID string, limit, offset int) (*dtomaterial.ListNodesResp, error)
	GetNode(ctx context.Context, id string) (*dtomaterial.NodeDetailResp, error)
	Upload(ctx context.Context, fileName, declaredName, declaredMime string, parentID string, r io.Reader) (*dtomaterial.NodeDetailResp, error)
	DrivePath(ctx context.Context, nodeID string) (string, error)
}

// Handler 是资料库的工具面实现（满足 mcp.Handler）。
type Handler struct {
	svc Service
}

// New 构造 Handler（svc 由装配根/子命令交下来）。
func New(svc Service) *Handler { return &Handler{svc: svc} }

// 工具描述（会给模型看；**刻意不含「语义/检索/知识库」字样**，G6 守卫钉住）。
const (
	descListNodes = "列出资料库目录下一层子节点。可选 parent_id 指定父节点（空=空间根），limit 指定返回条数。返回 [{id,parent_id,node_kind,title,size_bytes,updated_at}]。"
	descNodeInfo  = "读取一个资料库节点的信息与载体。可选 node_id。返回 {id,node_kind,title,body?,table_id?,schema?,source_url?,drive_path?,size_bytes}；drive 节点给 drive_path（本地只读绝对路径，交给 read 工具读取内容）。"
	descUpload    = "把一个本地文件上传为资料库节点（一次一个）。参数 path=本地绝对路径（必须在工作区或可信目录内），parent_id 可选（目标文件夹节点 id），file_name 可选（展示名，扩展名须与文件一致）。返回 {node_id,url}。"
)

// ListTools 返回三个文件级工具（无 search）。
func (h *Handler) ListTools(ctx context.Context) ([]protocol.McpTool, error) {
	return []protocol.McpTool{
		{
			Name:        ToolListNodes,
			Description: descListNodes,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"parent_id":{"type":"string","description":"父节点 id；空=空间根"},"limit":{"type":"integer","description":"返回条数上限"}},"additionalProperties":false}`),
		},
		{
			Name:        ToolNodeInfo,
			Description: descNodeInfo,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"node_id":{"type":"string","description":"资料库节点 id"}},"required":["node_id"],"additionalProperties":false}`),
		},
		{
			Name:        ToolUpload,
			Description: descUpload,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"本地文件绝对路径"},"parent_id":{"type":"string","description":"目标文件夹节点 id"},"file_name":{"type":"string","description":"展示名"}},"required":["path"],"additionalProperties":false}`),
		},
	}, nil
}

// CallTool 分发到具体工具。未知工具 → mcp.ErrToolNotFound（协议层 -32602）。
func (h *Handler) CallTool(ctx context.Context, name string, args json.RawMessage) (protocol.McpCallResult, error) {
	switch name {
	case ToolListNodes:
		return h.callListNodes(ctx, args)
	case ToolNodeInfo:
		return h.callNodeInfo(ctx, args)
	case ToolUpload:
		return h.callUpload(ctx, args)
	default:
		return protocol.McpCallResult{}, mcp.ErrToolNotFound
	}
}

// textResult 把任意值编成一段 JSON 文本块（MCP 文本内容）。
func textResult(v any) (protocol.McpCallResult, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return protocol.McpCallResult{}, err
	}
	blk, _ := json.Marshal(map[string]string{"type": "text", "text": string(raw)})
	return protocol.McpCallResult{Content: []json.RawMessage{blk}}, nil
}

// errResult 把业务错误编成 isError 结果（模型看得到原因，会话不炸）。
func errResult(err error) protocol.McpCallResult {
	text := err.Error()
	blk, _ := json.Marshal(map[string]string{"type": "text", "text": "错误：" + text})
	return protocol.McpCallResult{Content: []json.RawMessage{blk}, IsError: true}
}

func (h *Handler) callListNodes(ctx context.Context, args json.RawMessage) (protocol.McpCallResult, error) {
	var in struct {
		ParentID string `json:"parent_id"`
		Limit    int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &in); err != nil {
			return errResult(fmt.Errorf("参数不是合法 JSON: %w", err)), nil
		}
	}
	resp, err := h.svc.ListNodes(ctx, in.ParentID, in.Limit, 0)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(resp)
}

func (h *Handler) callNodeInfo(ctx context.Context, args json.RawMessage) (protocol.McpCallResult, error) {
	var in struct {
		NodeID string `json:"node_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errResult(fmt.Errorf("参数不是合法 JSON: %w", err)), nil
	}
	if in.NodeID == "" {
		return errResult(fmt.Errorf("node_id 不能为空")), nil
	}
	d, err := h.svc.GetNode(ctx, in.NodeID)
	if err != nil {
		return errResult(err), nil
	}
	// drive 节点附本地只读绝对路径（交给原生 read）。
	out := map[string]any{
		"id":         d.ID,
		"node_kind":  d.NodeKind,
		"title":      d.Title,
		"size_bytes": d.SizeBytes,
	}
	if d.Body != "" {
		out["body"] = d.Body
	}
	if d.TableID != "" {
		out["table_id"] = d.TableID
	}
	if d.Schema != "" {
		out["schema"] = d.Schema
	}
	if d.SourceURL != "" {
		out["source_url"] = d.SourceURL
	}
	if d.NodeKind == "drive" {
		if p, perr := h.svc.DrivePath(ctx, d.ID); perr == nil {
			out["drive_path"] = p
		}
	}
	return textResult(out)
}

func (h *Handler) callUpload(ctx context.Context, args json.RawMessage) (protocol.McpCallResult, error) {
	var in struct {
		Path     string `json:"path"`
		ParentID string `json:"parent_id"`
		FileName string `json:"file_name"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return errResult(fmt.Errorf("参数不是合法 JSON: %w", err)), nil
	}
	if in.Path == "" {
		return errResult(fmt.Errorf("path 不能为空")), nil
	}
	if !isSafeLocalPath(in.Path) {
		return errResult(fmt.Errorf("path 必须是绝对路径且不含 .. 段")), nil
	}
	f, err := os.Open(in.Path)
	if err != nil {
		return errResult(fmt.Errorf("无法打开文件 %s: %w", in.Path, err)), nil
	}
	defer func() { _ = f.Close() }()
	declaredName := in.FileName
	d, err := h.svc.Upload(ctx, in.Path, declaredName, "", in.ParentID, f)
	if err != nil {
		return errResult(err), nil
	}
	return textResult(map[string]string{
		"node_id": d.ID,
		// url 用本地 view 路径占位（本期 page 的 view 在 P3；drive 节点给定 node 路径标识）。
		"url": "/materials/nodes/" + d.ID,
	})
}

// isSafeLocalPath 拒绝越界/可疑路径（与附件链路同纪律：只接受绝对路径、拒绝 `..` 段）。
//
// 真正的「工作区/可信目录」白名单在部署面（sessionruntime 的可信目录）收敛；工具面这一层先做
// 最基本的 fail-closed（绝对 + 无 `..`），避免把任意相对路径当工作区文件。
func isSafeLocalPath(p string) bool {
	if len(p) == 0 || p[0] != '/' {
		return false
	}
	// 拒绝任何 `..` 段。
	parts := splitPath(p)
	for _, seg := range parts {
		if seg == ".." {
			return false
		}
	}
	return true
}

// splitPath 按 '/' 切（不依赖 filepath，避免与平台无关的差异；Windows 路径不支持，见文档）。
func splitPath(p string) []string {
	var out []string
	cur := ""
	for _, r := range p {
		if r == '/' {
			out = append(out, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	out = append(out, cur)
	return out
}

// 断言：*Handler 满足 mcp.Handler。
var _ mcp.Handler = (*Handler)(nil)

// 断言：protocol 包被引用（mcp.Tool/CallResult 是 protocol 的别名；这里显式引用以固定形状）。
var _ = protocol.McpTool{}
