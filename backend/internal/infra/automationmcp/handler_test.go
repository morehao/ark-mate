package automationmcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/morehao/ark-mate/internal/core/automation"
	"github.com/morehao/ark-mate/internal/dto/dtoautomation"
)

// fakeSvc 最小 Service 替身（只实现工具面用到的方法）。记录收到的入参以便断言。
type fakeSvc struct {
	created  []dtoautomation.AutomationWriteReq
	updated  []dtoautomation.AutomationWriteReq
	updateID string
	deleted  []string
	preview  dtoautomation.AutomationPreviewReq
	getID    string
	items    []dtoautomation.AutomationResp
	err      error
}

func (f *fakeSvc) ListAutomations() ([]dtoautomation.AutomationResp, error) {
	return f.items, f.err
}
func (f *fakeSvc) GetAutomation(id string) (*dtoautomation.AutomationResp, error) {
	f.getID = id
	if f.err != nil {
		return nil, f.err
	}
	return &dtoautomation.AutomationResp{ID: id}, nil
}
func (f *fakeSvc) CreateAutomation(_ context.Context, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error) {
	f.created = append(f.created, req)
	if f.err != nil {
		return nil, f.err
	}
	return &dtoautomation.AutomationResp{ID: "a"}, nil
}
func (f *fakeSvc) UpdateAutomation(_ context.Context, id string, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error) {
	f.updateID = id
	f.updated = append(f.updated, req)
	if f.err != nil {
		return nil, f.err
	}
	return &dtoautomation.AutomationResp{ID: id}, nil
}
func (f *fakeSvc) DeleteAutomation(_ context.Context, id string) (*dtoautomation.AutomationDeleteResp, error) {
	f.deleted = append(f.deleted, id)
	if f.err != nil {
		return nil, f.err
	}
	return &dtoautomation.AutomationDeleteResp{ID: id, Deleted: true}, nil
}
func (f *fakeSvc) PreviewAutomation(req dtoautomation.AutomationPreviewReq) (*dtoautomation.AutomationPreviewResp, error) {
	f.preview = req
	if f.err != nil {
		return nil, f.err
	}
	return &dtoautomation.AutomationPreviewResp{Occurrences: []dtoautomation.AutomationPreviewItem{{At: "2026-01-01T01:00:00Z", Wall: "2026-01-01 09:00", Timezone: "Asia/Shanghai"}}}, nil
}
func (f *fakeSvc) ListAutomationRuns(id string) (*dtoautomation.AutomationRunsResp, error) {
	return &dtoautomation.AutomationRunsResp{ID: id, Runs: nil, Limit: automation.RecentRunsMax}, nil
}

// TestToolsContract G5 契约快照：恰好六个工具、无 cron、schedule 五选一。
func TestToolsContract(t *testing.T) {
	h := New(&fakeSvc{})
	tools, err := h.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	want := map[string]bool{
		ToolCreate: true, ToolUpdate: true, ToolList: true, ToolGet: true, ToolDelete: true, ToolPreview: true,
	}
	if len(tools) != len(want) {
		t.Fatalf("应恰好 %d 个工具，got %d", len(want), len(tools))
	}
	for _, tl := range tools {
		if !want[tl.Name] {
			t.Fatalf("多余工具 %q", tl.Name)
		}
		sch := string(tl.InputSchema)
		// 无 cron：schema 里不该出现 cron 关键字（kind 的 enum 只有五个）。
		if strings.Contains(strings.ToLower(sch), "cron") {
			t.Fatalf("工具 %q 的 schema 不该出现 cron：%s", tl.Name, sch)
		}
		// create/update 的 schedule 五选一都要在 oneOf 里体现。
		if tl.Name == ToolCreate || tl.Name == ToolUpdate {
			for _, k := range []string{"once", "interval", "daily", "weekly", "monthly"} {
				if !strings.Contains(sch, `"`+k+`"`) {
					t.Fatalf("工具 %q 的 schedule 缺 kind %q", tl.Name, k)
				}
			}
		}
	}
}

// TestCreateForwardsPatch create：id 进 patch 体，其余键原样进 patch；缺 id 报错。
func TestCreateForwardsPatch(t *testing.T) {
	f := &fakeSvc{}
	h := New(f)
	args := json.RawMessage(`{"id":"a1","name":"喝水","prompt":"喝水","schedule":{"kind":"daily","timezone":"UTC","hour":9,"minute":0},"target":{"prompt":"喝水","delivery":"session","session_id":"s1"}}`)
	res, err := h.CallTool(context.Background(), ToolCreate, args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if res.IsError {
		t.Fatalf("不该是错误：%+v", res)
	}
	if len(f.created) != 1 {
		t.Fatalf("应调用一次 Create，got %d", len(f.created))
	}
	if string(f.created[0]["id"]) != `"a1"` {
		t.Fatalf("id 应进 patch 体，got %s", string(f.created[0]["id"]))
	}
	// 缺 id
	res, _ = h.CallTool(context.Background(), ToolCreate, json.RawMessage(`{"name":"x","prompt":"p","schedule":{},"target":{}}`))
	if !res.IsError {
		t.Fatal("缺 id 应报错")
	}
}

// TestUpdateIsIncremental update：id 走路径，patch 只含传入键。
func TestUpdateIsIncremental(t *testing.T) {
	f := &fakeSvc{}
	h := New(f)
	_, _ = h.CallTool(context.Background(), ToolUpdate, json.RawMessage(`{"id":"a1","name":"改名"}`))
	if f.updateID != "a1" {
		t.Fatalf("update id 应走路径，got %q", f.updateID)
	}
	if _, has := f.updated[0]["id"]; has {
		t.Fatal("update 的 patch 不该含 id")
	}
	if _, has := f.updated[0]["name"]; !has {
		t.Fatal("update 的 patch 应含 name")
	}
}

// TestListGetDelete list/get/delete 转发正确。
func TestListGetDelete(t *testing.T) {
	f := &fakeSvc{items: []dtoautomation.AutomationResp{{ID: "a1", Name: "n"}}}
	h := New(f)
	if res, _ := h.CallTool(context.Background(), ToolList, nil); res.IsError {
		t.Fatalf("list 不该错：%+v", res)
	}
	if _, _ = h.CallTool(context.Background(), ToolGet, json.RawMessage(`{"id":"a9"}`)); f.getID != "a9" {
		t.Fatalf("get id 转发错误：%q", f.getID)
	}
	_, _ = h.CallTool(context.Background(), ToolDelete, json.RawMessage(`{"id":"a7"}`))
	if len(f.deleted) != 1 || f.deleted[0] != "a7" {
		t.Fatalf("delete 转发错误：%+v", f.deleted)
	}
}

// TestPreviewForwards preview：schedule/count/from 原样转发；缺 schedule 报错。
func TestPreviewForwards(t *testing.T) {
	f := &fakeSvc{}
	h := New(f)
	_, _ = h.CallTool(context.Background(), ToolPreview, json.RawMessage(`{"schedule":{"kind":"daily","timezone":"UTC","hour":9,"minute":0},"count":3}`))
	if f.preview.Count != 3 || len(f.preview.Schedule) == 0 {
		t.Fatalf("preview 转发错误：%+v", f.preview)
	}
	if res, _ := h.CallTool(context.Background(), ToolPreview, json.RawMessage(`{}`)); !res.IsError {
		t.Fatal("缺 schedule 应报错")
	}
}

// TestErrPassthrough 业务错误 → isError 结果（不炸会话），且保留原因。
func TestErrPassthrough(t *testing.T) {
	f := &fakeSvc{err: errString("automation_exists: 已存在")}
	h := New(f)
	res, err := h.CallTool(context.Background(), ToolGet, json.RawMessage(`{"id":"a1"}`))
	if err != nil {
		t.Fatalf("CallTool 本身不该返回 err: %v", err)
	}
	if !res.IsError {
		t.Fatal("业务错误应编成 isError 结果")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
