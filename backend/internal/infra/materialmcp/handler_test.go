package materialmcp

import (
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtomaterial"
)

// fakeSvc 最小 MaterialSvc 替身（只实现工具面用到的方法）。
type fakeSvc struct {
	nodes    []dtomaterial.NodeResp
	detail   map[string]*dtomaterial.NodeDetailResp
	drivePth map[string]string
	uploaded []string
}

func (f *fakeSvc) ListNodes(_ context.Context, parent string, limit, offset int) (*dtomaterial.ListNodesResp, error) {
	var out []dtomaterial.NodeResp
	for _, n := range f.nodes {
		if n.ParentID == parent {
			out = append(out, n)
		}
	}
	return &dtomaterial.ListNodesResp{Nodes: out, Offset: offset, Limit: limit}, nil
}

func (f *fakeSvc) GetNode(_ context.Context, id string) (*dtomaterial.NodeDetailResp, error) {
	if d, ok := f.detail[id]; ok {
		return d, nil
	}
	return nil, errNotfound
}

func (f *fakeSvc) CreateNode(context.Context, dtomaterial.CreateNodeReq) (*dtomaterial.NodeDetailResp, error) {
	return nil, errNotfound
}
func (f *fakeSvc) PatchNode(context.Context, string, dtomaterial.PatchNodeReq) (*dtomaterial.NodeDetailResp, error) {
	return nil, errNotfound
}
func (f *fakeSvc) DeleteNode(context.Context, string) (*dtomaterial.DeleteNodeResp, error) {
	return &dtomaterial.DeleteNodeResp{}, nil
}
func (f *fakeSvc) Search(context.Context, string, int, int) (*dtomaterial.SearchResp, error) {
	return &dtomaterial.SearchResp{}, nil
}
func (f *fakeSvc) DrivePath(_ context.Context, id string) (string, error) {
	if p, ok := f.drivePth[id]; ok {
		return p, nil
	}
	return "", errNotfound
}
func (f *fakeSvc) Upload(_ context.Context, fileName, _ string, _ string, _ string, _ io.Reader) (*dtomaterial.NodeDetailResp, error) {
	f.uploaded = append(f.uploaded, fileName)
	return &dtomaterial.NodeDetailResp{ID: "libnd_up", NodeKind: "drive"}, nil
}

func (f *fakeSvc) ImportArtifact(_ context.Context, req dtomaterial.ImportArtifactReq) (*dtomaterial.NodeDetailResp, error) {
	if req.ArtifactID != "art_1" {
		return nil, errNotfound
	}
	return &dtomaterial.NodeDetailResp{
		ID: "libnd_art", NodeKind: "drive", Title: "简报.md",
		Category: "artifact", ProvenanceID: req.ArtifactID, ParentID: req.ParentID,
	}, nil
}

var errNotfound = &notFoundErr{}

type notFoundErr struct{}

func (*notFoundErr) Error() string { return "not found" }

// TestToolListShape（G5/G6）工具面恰三个文件级工具，**不含 search**，且描述不含语义/知识库字样。
func TestToolListShape(t *testing.T) {
	h := New(&fakeSvc{})
	tools, err := h.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var names []string
	for _, tl := range tools {
		names = append(names, tl.Name)
	}
	sort.Strings(names)
	want := []string{ToolListNodes, ToolNodeInfo, ToolUpload, ToolImportArtifact}
	sort.Strings(want)
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("工具面不符：got %v want %v（本期只做资料库、不做知识库——无 search）", names, want)
	}
	// G6：任何工具名/描述都不得出现语义/知识库字样（本期范围守卫的 MCP 面）。
	for _, tl := range tools {
		low := strings.ToLower(tl.Name + " " + tl.Description)
		for _, banned := range []string{"search", "semantic", "embedding", "vector", "知识库", "语义", "向量"} {
			if strings.Contains(low, banned) {
				t.Fatalf("工具 %q 的名称/描述含禁用词 %q（本期不做知识库/语义检索）", tl.Name, banned)
			}
		}
	}
}

// TestUnknownToolReturnsErrToolNotFound 未知工具 → mcp.ErrToolNotFound（协议层 -32602）。
func TestUnknownToolReturnsErrToolNotFound(t *testing.T) {
	h := New(&fakeSvc{})
	_, err := h.CallTool(context.Background(), "library_search", nil)
	if err == nil {
		t.Fatal("未知工具 library_search 应返回 ErrToolNotFound（本期无 search 工具）")
	}
}

// TestNodeInfoDrivePath drive 节点的 node_info 附本地只读路径。
func TestNodeInfoDrivePath(t *testing.T) {
	h := New(&fakeSvc{
		detail: map[string]*dtomaterial.NodeDetailResp{
			"libnd_x": {ID: "libnd_x", NodeKind: "drive", Title: "报告.pdf", DriveUploadID: "att_1"},
		},
		drivePth: map[string]string{"libnd_x": "/tmp/attachments/report.pdf"},
	})
	args, _ := json.Marshal(map[string]string{"node_id": "libnd_x"})
	res, err := h.CallTool(context.Background(), ToolNodeInfo, args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	txt := res.TextOf()
	if !strings.Contains(txt, "/tmp/attachments/report.pdf") {
		t.Fatalf("drive 节点 node_info 应含本地路径: %s", txt)
	}
}

// TestUploadRejectsUnsafePath 拒绝相对路径 / 含 .. 的路径（与附件链路同纪律）。
func TestUploadRejectsUnsafePath(t *testing.T) {
	h := New(&fakeSvc{})
	for _, bad := range []string{"relative/path.txt", "/tmp/../etc/passwd", ""} {
		args, _ := json.Marshal(map[string]string{"path": bad})
		res, err := h.CallTool(context.Background(), ToolUpload, args)
		if err != nil {
			t.Fatalf("CallTool 不应返回协议错误: %v", err)
		}
		if !res.IsError {
			t.Fatalf("不安全路径 %q 应返回 isError 结果", bad)
		}
	}
}

// g5Contract 三工具的入参/出参契约快照（加/删/改工具必须同批更新本表）。
var g5Contract = map[string][]string{
	ToolListNodes:      {"parent_id", "limit"},
	ToolNodeInfo:       {"node_id"},
	ToolUpload:         {"path", "parent_id", "file_name"},
	ToolImportArtifact: {"artifact_id", "parent_id"},
}

// TestToolInputSchemaFrozen（G5）各工具的 inputSchema properties 键集合冻结。
func TestToolInputSchemaFrozen(t *testing.T) {
	h := New(&fakeSvc{})
	tools, _ := h.ListTools(context.Background())
	for _, tl := range tools {
		want, ok := g5Contract[tl.Name]
		if !ok {
			t.Fatalf("工具 %q 不在 G5 契约快照里（新增工具必须同批更新）", tl.Name)
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(tl.InputSchema, &schema); err != nil {
			t.Fatalf("%s 的 inputSchema 不是合法 JSON: %v", tl.Name, err)
		}
		var got []string
		for k := range schema.Properties {
			got = append(got, k)
		}
		sort.Strings(got)
		sorted := append([]string(nil), want...)
		sort.Strings(sorted)
		if strings.Join(got, ",") != strings.Join(sorted, ",") {
			t.Fatalf("%s 的入参键集合变了：got %v want %v", tl.Name, got, sorted)
		}
	}
}

// TestImportArtifactTool 收录工具把参数原样交给用例层，并把节点身份回给模型。
func TestImportArtifactTool(t *testing.T) {
	h := New(&fakeSvc{})
	args, _ := json.Marshal(map[string]string{"artifact_id": "art_1", "parent_id": "libnd_dir"})
	res, err := h.CallTool(context.Background(), ToolImportArtifact, args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("收录应成功: %s", res.TextOf())
	}
	txt := res.TextOf()
	for _, want := range []string{"libnd_art", "art_1", "artifact"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("收录结果缺 %q: %s", want, txt)
		}
	}
}

// TestImportArtifactRequiresID artifact_id 必填（空 → isError，不调用例层）。
func TestImportArtifactRequiresID(t *testing.T) {
	h := New(&fakeSvc{})
	res, err := h.CallTool(context.Background(), ToolImportArtifact, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("CallTool 不应返回协议错误: %v", err)
	}
	if !res.IsError {
		t.Fatal("缺 artifact_id 应返回 isError")
	}
}
