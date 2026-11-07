package capabilitymcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
)

// stubSvc 记录调用并返回固定结果。
type stubSvc struct {
	lastCreate bool
	lastPathID string
	lastPatch  dtocapability.ExpertWriteReq
	lastEnable *bool
	calls      []string
	err        error
}

func (s *stubSvc) ListExperts(context.Context) ([]dtocapability.ExpertResp, error) {
	s.calls = append(s.calls, "list")
	if s.err != nil {
		return nil, s.err
	}
	return []dtocapability.ExpertResp{{ID: "a", Name: "A", Enabled: true}}, nil
}

func (s *stubSvc) GetExpert(_ context.Context, id string) (*dtocapability.ExpertResp, error) {
	s.calls = append(s.calls, "get:"+id)
	if s.err != nil {
		return nil, s.err
	}
	return &dtocapability.ExpertResp{ID: id, Name: "N", Instructions: "persona"}, nil
}

func (s *stubSvc) WriteExpert(_ context.Context, pathID string, req dtocapability.ExpertWriteReq, create bool) (*dtocapability.ExpertWriteResp, error) {
	s.calls = append(s.calls, "write")
	s.lastCreate = create
	s.lastPathID = pathID
	s.lastPatch = req
	if s.err != nil {
		return nil, s.err
	}
	return &dtocapability.ExpertWriteResp{ID: "x", Path: "expert:x", Created: create}, nil
}

func (s *stubSvc) DeleteExpert(_ context.Context, id string) (*dtocapability.ExpertDeleteResp, error) {
	s.calls = append(s.calls, "delete:"+id)
	if s.err != nil {
		return nil, s.err
	}
	return &dtocapability.ExpertDeleteResp{ID: id, Deleted: true}, nil
}

func (s *stubSvc) SetExpertEnabled(_ context.Context, id string, enabled bool) (*dtocapability.ExpertToggleResp, error) {
	s.calls = append(s.calls, "enable:"+id)
	s.lastEnable = &enabled
	if s.err != nil {
		return nil, s.err
	}
	return &dtocapability.ExpertToggleResp{ID: id, Enabled: enabled}, nil
}

func TestListToolsExposesAllSix(t *testing.T) {
	h := New(&stubSvc{})
	tools, err := h.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		ToolCreate: false, ToolUpdate: false, ToolList: false,
		ToolGet: false, ToolDelete: false, ToolSetEnabled: false,
	}
	for _, tl := range tools {
		if _, ok := want[tl.Name]; !ok {
			t.Fatalf("意外工具 %q", tl.Name)
		}
		want[tl.Name] = true
		if len(tl.InputSchema) == 0 || !json.Valid(tl.InputSchema) {
			t.Fatalf("工具 %q 的 InputSchema 非法 JSON: %s", tl.Name, tl.InputSchema)
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("缺少工具 %q", name)
		}
	}
}

// TestSchemaMatchesValidateExpert 把 schema 的关键约束钉住，防手滑偏离 ValidateExpert。
func TestSchemaMatchesValidateExpert(t *testing.T) {
	h := New(&stubSvc{})
	tools, _ := h.ListTools(context.Background())
	byName := map[string]string{}
	for _, tl := range tools {
		byName[tl.Name] = string(tl.InputSchema)
	}
	create := byName[ToolCreate]
	for _, needle := range []string{
		`"pattern":"^[a-z0-9]+(-[a-z0-9]+)*$"`, // id 正则
		`"maxLength":64`,                       // id ≤64
		`"maxLength":40`,                       // name 1-40
		`"maxLength":120`,                      // description ≤120
		`"maxLength":4096`,                     // instructions ≤4096
		`"pattern":"^mdl_"`,                    // model 前缀
		`"maxItems":50`,                        // skills/mcps ≤50
		`"maxItems":8`,                         // tags ≤8
		`"required":["id","name"]`,
	} {
		if !strings.Contains(create, needle) {
			t.Fatalf("create schema 缺少 %q\n%s", needle, create)
		}
	}
	// avatar 枚举
	for _, a := range []string{"spark", "chart", "code", "pen", "search", "briefcase"} {
		if !strings.Contains(create, `"`+a+`"`) {
			t.Fatalf("create schema 缺少 avatar 枚举 %q", a)
		}
	}
	if !strings.Contains(byName[ToolUpdate], `"required":["id"]`) {
		t.Fatalf("update schema 应以 id 为唯一 required: %s", byName[ToolUpdate])
	}
}

func TestCallCreateUsesCreateBranch(t *testing.T) {
	svc := &stubSvc{}
	h := New(svc)
	res, err := h.CallTool(context.Background(), ToolCreate,
		json.RawMessage(`{"id":"code-reviewer","name":"代码审查员","skills":["a","b"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("不应失败: %s", res.TextOf())
	}
	if !svc.lastCreate {
		t.Fatal("create 应走 create=true 分支")
	}
	if svc.lastPathID != "" {
		t.Fatalf("create 的 pathID 应为空，got %q", svc.lastPathID)
	}
	if _, ok := svc.lastPatch["id"]; !ok {
		t.Fatal("create 的 patch 必须含 id（写面从 patch 取 id）")
	}
}

func TestCallUpdateRoutesPathID(t *testing.T) {
	svc := &stubSvc{}
	h := New(svc)
	_, err := h.CallTool(context.Background(), ToolUpdate,
		json.RawMessage(`{"id":"abc","name":"新名"}`))
	if err != nil {
		t.Fatal(err)
	}
	if svc.lastCreate {
		t.Fatal("update 应走 create=false")
	}
	if svc.lastPathID != "abc" {
		t.Fatalf("update 的 pathID 应为 abc，got %q", svc.lastPathID)
	}
	if _, ok := svc.lastPatch["id"]; ok {
		t.Fatal("update 的 patch 不应含 id（id 走 pathID）")
	}
}

func TestCallSetEnabledRequiresFlag(t *testing.T) {
	svc := &stubSvc{}
	h := New(svc)
	res, _ := h.CallTool(context.Background(), ToolSetEnabled, json.RawMessage(`{"id":"abc"}`))
	if !res.IsError {
		t.Fatal("缺 enabled 应报错")
	}
	res, _ = h.CallTool(context.Background(), ToolSetEnabled, json.RawMessage(`{"id":"abc","enabled":false}`))
	if res.IsError {
		t.Fatalf("不应失败: %s", res.TextOf())
	}
	if svc.lastEnable == nil || *svc.lastEnable {
		t.Fatal("enabled=false 应传到服务端")
	}
}

func TestCallUnknownTool(t *testing.T) {
	h := New(&stubSvc{})
	res, err := h.CallTool(context.Background(), "nope_tool", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("未知工具应返回 isError 结果")
	}
}

func TestServiceErrorSurfacesAsIsError(t *testing.T) {
	svc := &stubSvc{err: errors.New("definition_invalid: 含危险标记")}
	h := New(svc)
	res, err := h.CallTool(context.Background(), ToolCreate, json.RawMessage(`{"id":"x","name":"y"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("服务端拒绝应表现为 isError 结果（会话不炸）")
	}
	if !strings.Contains(res.TextOf(), "危险标记") {
		t.Fatalf("错误原因应透传，got %s", res.TextOf())
	}
}
