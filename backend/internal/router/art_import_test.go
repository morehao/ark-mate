package router_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtomaterial"
)

// TestArtifactImportFlow（A-3）：产物登记 → 收录进资料库的端到端不变量。
//
//  1. POST /files 收字节 → file_id；
//  2. POST /artifacts 登记产物（file 类）；
//  3. POST /materials/import-artifact 收录 → 节点 category='artifact' 且 provenance_id 非空（同真同假）；
//  4. 幂等：二次收录返回同一节点（不重复建）；
//  5. 悬空 artifact_id → 40401 artifact_not_found。
func TestArtifactImportFlow(t *testing.T) {
	e, _ := materialApp(t)

	// 1) 收字节
	content := []byte("report body")
	sum := sha256.Sum256(content)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("content_hash", hex.EncodeToString(sum[:]))
	fw, _ := mw.CreateFormFile("file", "report.txt")
	_, _ = fw.Write(content)
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/v1/mate/files", &buf)
	req.Header.Set("content-type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	var fileEnv struct {
		Code int `json:"code"`
		Data struct {
			FileID string `json:"file_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &fileEnv)
	if fileEnv.Code != 0 || fileEnv.Data.FileID == "" {
		t.Fatalf("收字节失败 code=%d body=%s", fileEnv.Code, w.Body.String())
	}

	// 2) 登记产物（需要一个 session_id；用任意非空串——产物表不校验外键存在性）
	artifactBody := `{"file_id":"` + fileEnv.Data.FileID + `","session_id":"ses_test","origin":"tool_write","kind":"file","title":"报告"}`
	_, code, data := matCall(t, e, "POST", "/v1/mate/artifacts", artifactBody)
	if code != 0 {
		t.Fatalf("登记产物失败 code=%d body=%s", code, string(data))
	}
	var art struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(data, &art)
	if art.ID == "" {
		t.Fatalf("产物 id 为空: %s", string(data))
	}

	// 3) 收录进资料库
	_, code, nd := matCall(t, e, "POST", "/v1/mate/materials/import-artifact", `{"artifact_id":"`+art.ID+`"}`)
	if code != 0 {
		t.Fatalf("收录失败 code=%d body=%s", code, string(nd))
	}
	var node dtomaterial.NodeDetailResp
	_ = json.Unmarshal(nd, &node)
	// 不变量：category='artifact' ⟺ provenance_id 非空（同真同假）。
	if node.Category != "artifact" {
		t.Fatalf("收录节点 category 应为 artifact，got %q", node.Category)
	}
	if node.ProvenanceID != art.ID {
		t.Fatalf("收录节点 provenance_id 应为 %q，got %q", art.ID, node.ProvenanceID)
	}

	// 4) 幂等：二次收录返回同一节点 id。
	_, code2, nd2 := matCall(t, e, "POST", "/v1/mate/materials/import-artifact", `{"artifact_id":"`+art.ID+`"}`)
	if code2 != 0 {
		t.Fatalf("二次收录 code=%d", code2)
	}
	var node2 dtomaterial.NodeDetailResp
	_ = json.Unmarshal(nd2, &node2)
	if node2.ID != node.ID {
		t.Fatalf("二次收录应返回同一节点（幂等），got %q want %q", node2.ID, node.ID)
	}

	// 5) 悬空 artifact_id → 40401。
	_, code3, _ := matCall(t, e, "POST", "/v1/mate/materials/import-artifact", `{"artifact_id":"art_nope"}`)
	if code3 != 40401 {
		t.Fatalf("悬空 artifact_id 应收 40401，got %d", code3)
	}
}
