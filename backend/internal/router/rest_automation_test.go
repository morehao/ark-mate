package router_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/dto/dtoautomation"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/automation"
)

// ——— automation 面的控制器契约（M4-5）———
//
// 走**真实路由装配**（router.New）：路径、方法与中间件形态都与线上一致，
// 断言的是"每个端点的错误码"这条 Gate M4 验收项——所以重点不在 happy path，
// 而在"领域错误有没有被控制器原样落成对外码"。

func (s *stubSvc) ListAutomations() ([]dtoautomation.AutomationResp, error) {
	if s.autoErr != nil {
		return nil, s.autoErr
	}
	return s.automations, nil
}

func (s *stubSvc) GetAutomation(id string) (*dtoautomation.AutomationResp, error) {
	s.autoGot = id
	if s.autoErr != nil {
		return nil, s.autoErr
	}
	return &dtoautomation.AutomationResp{ID: id, Name: "巡检"}, nil
}

func (s *stubSvc) CreateAutomation(_ context.Context, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error) {
	s.autoAction = "create"
	s.autoReq = req
	if s.autoErr != nil {
		return nil, s.autoErr
	}
	return &dtoautomation.AutomationResp{ID: "daily"}, nil
}

func (s *stubSvc) UpdateAutomation(_ context.Context, id string, req dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error) {
	s.autoAction, s.autoID, s.autoReq = "update", id, req
	if s.autoErr != nil {
		return nil, s.autoErr
	}
	return &dtoautomation.AutomationResp{ID: id}, nil
}

func (s *stubSvc) DeleteAutomation(_ context.Context, id string) (*dtoautomation.AutomationDeleteResp, error) {
	s.autoAction, s.autoID = "delete", id
	if s.autoErr != nil {
		return nil, s.autoErr
	}
	return &dtoautomation.AutomationDeleteResp{ID: id, Deleted: true}, nil
}

func (s *stubSvc) PreviewAutomation(req dtoautomation.AutomationPreviewReq) (*dtoautomation.AutomationPreviewResp, error) {
	s.autoPreviewReq = req
	if s.autoErr != nil {
		return nil, s.autoErr
	}
	return &dtoautomation.AutomationPreviewResp{
		Occurrences: []dtoautomation.AutomationPreviewItem{
			{At: "2026-09-24T01:00:00Z", Wall: "2026-09-24 09:00", Timezone: "Asia/Shanghai"},
		},
	}, nil
}

func (s *stubSvc) ListAutomationRuns(id string) (*dtoautomation.AutomationRunsResp, error) {
	s.autoGot = id
	if s.autoErr != nil {
		return nil, s.autoErr
	}
	return &dtoautomation.AutomationRunsResp{
		ID: id, Limit: automation.RecentRunsMax,
		Runs: []automation.RunRecord{{Status: automation.RunOverlap}},
	}, nil
}

func (s *stubSvc) RunAutomationNow(_ context.Context, id string) (*dtoautomation.AutomationRunResp, error) {
	s.autoAction, s.autoID = "run", id
	if s.autoErr != nil {
		return nil, s.autoErr
	}
	return &dtoautomation.AutomationRunResp{ID: id, Status: automation.RunClaimed, Trigger: automation.TriggerManual}, nil
}

// autoEngine 只把 automation 端口替换成桩（其余端口仍由同一个 stubSvc 提供）。
func autoEngine(svc *stubSvc) *gin.Engine {
	deps := stubDeps(svc)
	deps.Automation = svc
	return newRestEngine(deps)
}

// TestAutomationRoutesHappyPath 8 条路由都能通、且控制器把参数原样交到领域层。
func TestAutomationRoutesHappyPath(t *testing.T) {
	svc := &stubSvc{automations: []dtoautomation.AutomationResp{{ID: "daily"}}}
	r := autoEngine(svc)

	env, _ := do(t, r, "GET", "/v1/mate/automations", "")
	if env.Code != 0 {
		t.Fatalf("列表应成功: %+v", env)
	}
	env, _ = do(t, r, "GET", "/v1/mate/automations/daily", "")
	if env.Code != 0 || svc.autoGot != "daily" {
		t.Fatalf("取单条应成功且透传 id: %+v got=%q", env, svc.autoGot)
	}
	env, _ = do(t, r, "POST", "/v1/mate/automations", `{"id":"daily","schedule":{"kind":"daily","timezone":"UTC","hour":9,"minute":0}}`)
	if env.Code != 0 || svc.autoAction != "create" {
		t.Fatalf("新建应成功: %+v action=%q", env, svc.autoAction)
	}
	// PUT 的"只改出现过的键"必须原样传到领域层（这里是原文对象，不是解好的结构体）
	env, _ = do(t, r, "PUT", "/v1/mate/automations/daily", `{"name":"改名"}`)
	if env.Code != 0 || svc.autoAction != "update" || svc.autoID != "daily" {
		t.Fatalf("覆盖写应成功: %+v action=%q id=%q", env, svc.autoAction, svc.autoID)
	}
	if _, ok := svc.autoReq["name"]; !ok || len(svc.autoReq) != 1 {
		t.Fatalf("请求体应只带 name 一个键（PUT 语义靠它）: %+v", svc.autoReq)
	}
	env, _ = do(t, r, "DELETE", "/v1/mate/automations/daily", "")
	if env.Code != 0 || svc.autoAction != "delete" {
		t.Fatalf("删除应成功: %+v action=%q", env, svc.autoAction)
	}
	env, _ = do(t, r, "GET", "/v1/mate/automations/daily/runs", "")
	if env.Code != 0 {
		t.Fatalf("运行史应成功: %+v", env)
	}
	env, _ = do(t, r, "POST", "/v1/mate/automations/daily/run", "")
	if env.Code != 0 || svc.autoAction != "run" {
		t.Fatalf("手动触发应成功: %+v action=%q", env, svc.autoAction)
	}
	env, _ = do(t, r, "POST", "/v1/mate/automations/preview", `{"schedule":{"kind":"daily","timezone":"Asia/Shanghai","hour":9,"minute":0}}`)
	if env.Code != 0 || len(svc.autoPreviewReq.Schedule) == 0 {
		t.Fatalf("预览应成功: %+v req=%+v", env, svc.autoPreviewReq)
	}
}

// TestAutomationPreviewIsNotAnID `preview` 必须走预览端点，而不是被 `:id` 吃掉。
//
// 这是注册顺序的真实风险：gin 会优先匹配静态段，但一旦有人把 preview 挪到 :id 之后，
// 表现是"预览神秘地报 40401 未知 automation preview"——所以用断言钉住。
func TestAutomationPreviewIsNotAnID(t *testing.T) {
	svc := &stubSvc{}
	r := autoEngine(svc)
	env, _ := do(t, r, "POST", "/v1/mate/automations/preview", `{"schedule":{"kind":"once","timezone":"UTC","at":"2099-01-01T00:00:00Z"}}`)
	if env.Code != 0 {
		t.Fatalf("preview 不该被当成 :id: %+v", env)
	}
	if svc.autoGot == "preview" {
		t.Fatal("preview 被路由到了 GetAutomation")
	}
}

// TestAutomationRoutesErrorCodes 每个端点的错误码：领域错误必须原样落成对外码（不是 code:-1）。
//
// 40000 与 40001 的分界沿用能力写面已确立的那条线：**请求形态**（缺字段/类型不符/body 不是对象）
// 是 40000，**取值语义**（保留键、id 非法、定义不可执行、未配置目录、已停用）是 40001。
// 两者混用会让"我该改参数还是改内容"这个问题在客户端无从判断。
func TestAutomationRoutesErrorCodes(t *testing.T) {
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		err    error
		want   int
	}{
		{"取单条不存在", "GET", "/v1/mate/automations/nope", "",
			acl.NewError(acl.ClassRequest, "automation_not_found", "未知 automation", nil), 40401},
		{"新建重复", "POST", "/v1/mate/automations", `{"id":"d"}`,
			acl.NewError(acl.ClassLimit, "automation_exists", "已存在", nil), 40901},
		{"写平台独占键", "PUT", "/v1/mate/automations/d", `{"last_fired_at":"2026-01-01T00:00:00Z"}`,
			acl.NewError(acl.ClassRequest, "invalid_param", "平台写回键不可写", nil), 40001},
		{"定义非法", "PUT", "/v1/mate/automations/d", `{"schedule":{"kind":"daily"}}`,
			acl.NewError(acl.ClassRequest, "definition_invalid", "定义不可执行", nil), 40001},
		{"未配置目录", "DELETE", "/v1/mate/automations/d", "",
			acl.NewError(acl.ClassRequest, "automation_dir_unset", "未配置目录", nil), 40001},
		{"预览非法 schedule", "POST", "/v1/mate/automations/preview", `{"schedule":{"kind":"daily"}}`,
			acl.NewError(acl.ClassRequest, "invalid_param", "缺时区", nil), 40001},
		{"手动触发已停用", "POST", "/v1/mate/automations/d/run", "",
			acl.NewError(acl.ClassRequest, "automation_disabled", "已停用", nil), 40001},
		{"运行史定义坏了", "GET", "/v1/mate/automations/d/runs", "",
			acl.NewError(acl.ClassRequest, "definition_invalid", "定义不可执行", nil), 40001},
		{"落盘冲突", "PUT", "/v1/mate/automations/d", `{"name":"x"}`,
			acl.NewError(acl.ClassLimit, "definition_changed", "请刷新重试", nil), 40901},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &stubSvc{autoErr: c.err}
			r := autoEngine(svc)
			env, _ := do(t, r, c.method, c.path, c.body)
			if env.Code != c.want {
				t.Fatalf("期望码 %d，got %d（msg=%s）", c.want, env.Code, env.Msg)
			}
		})
	}
}

// TestAutomationBindErrorIsInvalidParam body 不是合法 JSON / 不是对象 → 40000（不是 50000）。
func TestAutomationBindErrorIsInvalidParam(t *testing.T) {
	svc := &stubSvc{}
	r := autoEngine(svc)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/v1/mate/automations", `[1,2,3]`},
		{"PUT", "/v1/mate/automations/d", `not json`},
		{"POST", "/v1/mate/automations/preview", `"x"`},
	} {
		env, _ := do(t, r, c.method, c.path, c.body)
		if env.Code != 40000 {
			t.Fatalf("%s %s body=%s 期望 40000，got %d", c.method, c.path, c.body, env.Code)
		}
		if !strings.Contains(env.Msg, "参数错误") {
			t.Fatalf("文案应指出是参数问题: %s", env.Msg)
		}
	}
	// 领域层返回裸错误 → 由 code.FromError 兜成 50000（唯一兜底码），绝不透传
	svc2 := &stubSvc{autoErr: errors.New("boom")}
	r2 := autoEngine(svc2)
	env, _ := do(t, r2, "GET", "/v1/mate/automations", "")
	if env.Code != 50000 {
		t.Fatalf("裸错误应兜底 50000，got %d", env.Code)
	}
}
