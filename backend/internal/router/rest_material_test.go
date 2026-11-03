package router_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/bootstrap/assembly"
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/dto/dtomaterial"
	"github.com/morehao/ark-mate/internal/infra/attachmentstore"
	"github.com/morehao/ark-mate/internal/infra/dsh"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/ark-mate/internal/sessionruntime"
	"github.com/morehao/ark-mate/internal/testutil"
)

// materialApp 装配真路由 + 真 sqlite 库 + 真附件字节存储（资料库 P1 的进程内集成判据）。
//
// 与附件面共用 attachmentstore；Store 交进 Facts 后 material 走 sqlite 载体（非 memory 40501）。
func materialApp(t *testing.T) (*gin.Engine, *sessionruntime.Service) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbPath := testutil.TempDBPath(t)
	st := testutil.OpenStore(t, dbPath)
	t.Cleanup(func() { _ = st.Close() })

	// 附件字节存储（上传用例要它）。core_file* 由 filestore.Migrate 建（幂等）。
	if err := attachmentstore.Migrate(st.DB()); err != nil {
		t.Fatalf("filestore.Migrate: %v", err)
	}
	attachStore, err := attachmentstore.New(st.DB(), attachmentstore.Options{
		BaseDir:        filepath.Join(t.TempDir(), "attachments"),
		MaxUploadBytes: 20 << 20,
	})
	if err != nil {
		t.Fatalf("attachmentstore.New: %v", err)
	}

	reg := dsh.Registry()
	reg.ApplyIsolationEnv(t.TempDir())
	bus := eventbus.New()
	svc := sessionruntime.NewService(reg, bus)
	exec := runtimeconn.NewPlaneFor(reg)

	cfg := config.Default()
	cfg.HomeDir = t.TempDir() // page 发布落盘根：必须在私有临时目录（否则会写进测试 cwd）
	services := assembly.Facts{
		Cfg:         cfg,
		Store:       st,
		Planes:      svc.Planes(),
		Session:     svc,
		Bus:         bus,
		Runtime:     reg,
		Execution:   exec,
		AttachStore: attachStore,
	}.BuildServices()
	engine := router.New(router.Deps{
		Controllers: services.BuildControllers(nil, nil),
		FileStore:   attachStore.FileStore(),
	})
	return engine, svc
}

// matCall 发一次 JSON 请求，返回 (HTTP 状态, 信封 code, data)。
func matCall(t *testing.T, e *gin.Engine, method, path, body string) (int, int, json.RawMessage) {
	t.Helper()
	var bodyReader *bytes.Reader
	if body != "" {
		bodyReader = bytes.NewReader([]byte(body))
	} else {
		bodyReader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set("content-type", "application/json")
	}
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	var env struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("%s %s 响应非 JSON: %v body=%s", method, path, err, w.Body.String())
	}
	return w.Code, env.Code, env.Data
}

// TestMaterialHTTPFlow 建目录树 → 列 → 详情 → 改名/移动 → 关键词检索 → 删除级联，全走真实路由。
func TestMaterialHTTPFlow(t *testing.T) {
	e, _ := materialApp(t)

	// ① 建根目录 folder。
	_, code, data := matCall(t, e, "POST", "/v1/mate/materials/nodes", `{"node_kind":"folder","title":"项目资料"}`)
	if code != 0 {
		t.Fatalf("建 folder 失败 code=%d", code)
	}
	var folder dtomaterial.NodeDetailResp
	_ = json.Unmarshal(data, &folder)
	if folder.ID == "" || folder.NodeKind != "folder" {
		t.Fatalf("folder 结果不符: %+v", folder)
	}

	// ② 在 folder 下建 doc（带正文）。
	_, code, data = matCall(t, e, "POST", "/v1/mate/materials/nodes",
		`{"parent_id":"`+folder.ID+`","node_kind":"doc","title":"季度报告","body":"本季度营收增长"}`)
	if code != 0 {
		t.Fatalf("建 doc 失败 code=%d", code)
	}
	var doc dtomaterial.NodeDetailResp
	_ = json.Unmarshal(data, &doc)
	if doc.ParentID != folder.ID || doc.Body == "" {
		t.Fatalf("doc 结果不符: %+v", doc)
	}

	// ③ 列根目录 → 应有 folder 一项。
	_, code, data = matCall(t, e, "GET", "/v1/mate/materials/nodes", "")
	if code != 0 {
		t.Fatalf("列目录失败 code=%d", code)
	}
	var list dtomaterial.ListNodesResp
	_ = json.Unmarshal(data, &list)
	if len(list.Nodes) != 1 || list.Nodes[0].ID != folder.ID {
		t.Fatalf("根目录应只有 folder: %+v", list.Nodes)
	}

	// ④ 列 folder 子节点 → 应有 doc。
	_, _, data = matCall(t, e, "GET", "/v1/mate/materials/nodes?parent="+folder.ID, "")
	_ = json.Unmarshal(data, &list)
	if len(list.Nodes) != 1 || list.Nodes[0].ID != doc.ID {
		t.Fatalf("folder 下应有 doc: %+v", list.Nodes)
	}

	// ⑤ 改名 doc。
	_, code, data = matCall(t, e, "PATCH", "/v1/mate/materials/nodes/"+doc.ID, `{"title":"Q3 季度报告"}`)
	if code != 0 {
		t.Fatalf("改名失败 code=%d", code)
	}
	var renamed dtomaterial.NodeDetailResp
	_ = json.Unmarshal(data, &renamed)
	if renamed.Title != "Q3 季度报告" {
		t.Fatalf("改名结果不符: %+v", renamed)
	}

	// ⑥ 关键词检索命中（title/body 子串），且不含语义字段。
	_, code, data = matCall(t, e, "GET", "/v1/mate/materials/search?q="+"报告", "")
	if code != 0 {
		t.Fatalf("检索失败 code=%d", code)
	}
	var sresp dtomaterial.SearchResp
	_ = json.Unmarshal(data, &sresp)
	if sresp.Total != 1 || sresp.Hits[0].NodeID != doc.ID {
		t.Fatalf("检索应命中 doc: %+v", sresp)
	}

	// ⑦ 删除 folder → 级联删 doc；再列根目录为空。
	_, code, _ = matCall(t, e, "DELETE", "/v1/mate/materials/nodes/"+folder.ID, "")
	if code != 0 {
		t.Fatalf("删 folder 失败 code=%d", code)
	}
	_, _, data = matCall(t, e, "GET", "/v1/mate/materials/nodes", "")
	_ = json.Unmarshal(data, &list)
	if len(list.Nodes) != 0 {
		t.Fatalf("级联删后根目录应为空: %+v", list.Nodes)
	}
	// 幂等删。
	_, code, data = matCall(t, e, "DELETE", "/v1/mate/materials/nodes/"+folder.ID, "")
	var del dtomaterial.DeleteNodeResp
	_ = json.Unmarshal(data, &del)
	if code != 0 || del.Deleted {
		t.Fatalf("重复删应幂等 deleted=false: code=%d del=%+v", code, del)
	}
}

// TestMaterialErrorCodes fail-closed 错误码：未知节点 / 非默认空间 / 载体不匹配 / 成环 / 空关键词。
func TestMaterialErrorCodes(t *testing.T) {
	e, _ := materialApp(t)

	// 未知节点 → 40401 material_node_not_found。
	_, code, _ := matCall(t, e, "GET", "/v1/mate/materials/nodes/libnd_nope", "")
	if code != 40401 {
		t.Fatalf("未知节点应 40401，got %d", code)
	}

	// 非默认 space → 40501 space_not_supported。
	_, code, _ = matCall(t, e, "POST", "/v1/mate/materials/nodes", `{"node_kind":"folder","title":"x","space_id":"team-1"}`)
	if code != 40501 {
		t.Fatalf("非默认 space 应 40501，got %d", code)
	}

	// doc 无 body → 40001 material_node_kind_mismatch。
	_, code, _ = matCall(t, e, "POST", "/v1/mate/materials/nodes", `{"node_kind":"doc","title":"no body"}`)
	if code != 40001 {
		t.Fatalf("doc 无 body 应 40001，got %d", code)
	}

	// 空关键词 → 40000。
	_, code, _ = matCall(t, e, "GET", "/v1/mate/materials/search?q=", "")
	if code != 40000 {
		t.Fatalf("空关键词应 40000，got %d", code)
	}

	// 成环：A 的父设成 B，再把 B 的父设成 A → 40001 material_cycle。
	_, _, da := matCall(t, e, "POST", "/v1/mate/materials/nodes", `{"node_kind":"folder","title":"A"}`)
	var a dtomaterial.NodeDetailResp
	_ = json.Unmarshal(da, &a)
	_, _, db := matCall(t, e, "POST", "/v1/mate/materials/nodes", `{"node_kind":"folder","title":"B","parent_id":"`+a.ID+`"}`)
	var b dtomaterial.NodeDetailResp
	_ = json.Unmarshal(db, &b)
	// 把 A 移到 B 之下 → 成环。
	_, code, _ = matCall(t, e, "PATCH", "/v1/mate/materials/nodes/"+a.ID, `{"parent_id":"`+b.ID+`"}`)
	if code != 40001 {
		t.Fatalf("成环移动应 40001，got %d", code)
	}
}

// TestMaterialUpload 上传文件成 drive 节点（P0′ 收口后两步式：POST /files 拿 file_id → POST /materials/nodes）。
func TestMaterialUpload(t *testing.T) {
	e, _ := materialApp(t)

	content := []byte("hello material")
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("content_hash", hash)
	fw, _ := mw.CreateFormFile("file", "notes.txt")
	_, _ = fw.Write(content)
	_ = mw.Close()

	// 第一步：POST /files 收字节（ginupload）。
	req := httptest.NewRequest("POST", "/v1/mate/files", &buf)
	req.Header.Set("content-type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("files HTTP %d: %s", w.Code, w.Body.String())
	}
	var fileEnv struct {
		Code int `json:"code"`
		Data struct {
			FileID string `json:"file_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &fileEnv)
	if fileEnv.Code != 0 || fileEnv.Data.FileID == "" {
		t.Fatalf("files code=%d body=%s", fileEnv.Code, w.Body.String())
	}

	// 第二步：POST /materials/nodes {node_kind:drive, drive_upload_id:file_id}。
	body := `{"node_kind":"drive","title":"notes.txt","drive_upload_id":"` + fileEnv.Data.FileID + `"}`
	_, code, data := matCall(t, e, "POST", "/v1/mate/materials/nodes", body)
	if code != 0 {
		t.Fatalf("建 drive 节点 code=%d body=%s", code, string(data))
	}
	var node dtomaterial.NodeDetailResp
	_ = json.Unmarshal(data, &node)
	if node.NodeKind != "drive" || node.DriveUploadID == "" {
		t.Fatalf("两步式应落 drive 节点且带 drive_upload_id: %+v", node)
	}
}

// TestMaterialTableFlow 建表 → 读行 → 整表替换行 → 再读一致（P3）。
func TestMaterialTableFlow(t *testing.T) {
	e, _ := materialApp(t)

	_, code, data := matCall(t, e, "POST", "/v1/mate/materials/nodes",
		`{"node_kind":"table","title":"账目","schema":"[{\"name\":\"金额\",\"type\":\"number\"}]"}`)
	if code != 0 {
		t.Fatalf("建 table 失败 code=%d", code)
	}
	var tbl dtomaterial.NodeDetailResp
	_ = json.Unmarshal(data, &tbl)
	if tbl.NodeKind != "table" || tbl.Schema == "" {
		t.Fatalf("table 结果不符: %+v", tbl)
	}

	// 初始空行。
	_, code, data = matCall(t, e, "GET", "/v1/mate/materials/nodes/"+tbl.ID+"/rows", "")
	if code != 0 {
		t.Fatalf("读行失败 code=%d", code)
	}
	var rr dtomaterial.RowsResp
	_ = json.Unmarshal(data, &rr)
	if rr.Rows != "[]" {
		t.Fatalf("新表应为空行数组: %q", rr.Rows)
	}

	// 整表替换。
	_, code, data = matCall(t, e, "PUT", "/v1/mate/materials/nodes/"+tbl.ID+"/rows",
		`{"rows":"[{\"金额\":100},{\"金额\":200}]"}`)
	if code != 0 {
		t.Fatalf("写行失败 code=%d", code)
	}
	_ = json.Unmarshal(data, &rr)
	if !strings.Contains(rr.Rows, "100") {
		t.Fatalf("写行后应有 100: %q", rr.Rows)
	}

	// 再读一致。
	_, _, data = matCall(t, e, "GET", "/v1/mate/materials/nodes/"+tbl.ID+"/rows", "")
	_ = json.Unmarshal(data, &rr)
	if !strings.Contains(rr.Rows, "200") {
		t.Fatalf("再读应含 200: %q", rr.Rows)
	}

	// 非法 rows（不是数组）→ 40000。
	_, code, _ = matCall(t, e, "PUT", "/v1/mate/materials/nodes/"+tbl.ID+"/rows", `{"rows":"{}"}`)
	if code != 40000 {
		t.Fatalf("非数组 rows 应 40000，got %d", code)
	}
}

// TestMaterialPagePublishView 建 page → publish → view 回 HTML（P3）；非 page 拒绝。
func TestMaterialPagePublishView(t *testing.T) {
	e, _ := materialApp(t)

	_, code, data := matCall(t, e, "POST", "/v1/mate/materials/nodes",
		`{"node_kind":"page","title":"看板","body":"<html><body>hi</body></html>"}`)
	if code != 0 {
		t.Fatalf("建 page 失败 code=%d", code)
	}
	var pg dtomaterial.NodeDetailResp
	_ = json.Unmarshal(data, &pg)

	_, code, data = matCall(t, e, "POST", "/v1/mate/materials/nodes/"+pg.ID+"/publish", "")
	if code != 0 {
		t.Fatalf("publish 失败 code=%d", code)
	}
	var pub dtomaterial.PublishPageResp
	_ = json.Unmarshal(data, &pub)
	if pub.FilePath == "" || pub.ViewPath == "" {
		t.Fatalf("publish 应给 file_path + view_path: %+v", pub)
	}

	// view 回 HTML。
	req := httptest.NewRequest("GET", "/v1/mate/materials/nodes/"+pg.ID+"/view", nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "hi") {
		t.Fatalf("view 应回 200 + HTML: code=%d body=%s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("view Content-Type 应 text/html: %q", ct)
	}

	// 非 page 发布 → 40001。
	_, _, d2 := matCall(t, e, "POST", "/v1/mate/materials/nodes", `{"node_kind":"doc","title":"d","body":"x"}`)
	var doc dtomaterial.NodeDetailResp
	_ = json.Unmarshal(d2, &doc)
	_, code, _ = matCall(t, e, "POST", "/v1/mate/materials/nodes/"+doc.ID+"/publish", "")
	if code != 40001 {
		t.Fatalf("非 page 发布应 40001，got %d", code)
	}
}

// TestMaterialLinkCreate 建 link 节点（body=剪藏 md + source_url）。
func TestMaterialLinkCreate(t *testing.T) {
	e, _ := materialApp(t)
	_, code, data := matCall(t, e, "POST", "/v1/mate/materials/nodes",
		`{"node_kind":"link","title":"剪藏","body":"# 快照","source_url":"https://example.com"}`)
	if code != 0 {
		t.Fatalf("建 link 失败 code=%d", code)
	}
	var ln dtomaterial.NodeDetailResp
	_ = json.Unmarshal(data, &ln)
	if ln.NodeKind != "link" || ln.SourceURL != "https://example.com" || ln.Body == "" {
		t.Fatalf("link 结果不符: %+v", ln)
	}
	// link 缺 source_url → 40001（载体一致性）。
	_, code, _ = matCall(t, e, "POST", "/v1/mate/materials/nodes", `{"node_kind":"link","title":"x","body":"y"}`)
	if code != 40001 {
		t.Fatalf("link 缺 url 应 40001，got %d", code)
	}
}
