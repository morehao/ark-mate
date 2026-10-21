package ctrautomation

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/platform/code"
	"github.com/morehao/ark-mate/internal/contract/dtoautomation"
)

// ——— 控制器的**错误出口**：领域错误原样落码，裸错误兜底 50000 ———
//
// 这两条判据原先混在 `router/rest_automation_test.go` 的替身表里（那份替身给什么都行）。
// P4a 正文第九刀把 automation 那一格换成**真平面**之后，路由器侧再也造不出这两条——
// 它们验的本来也不是领域行为，而是**本控制器**的出口：`gincontext.Fail(c, code.FromError(err))`。
// 所以搬到这里，并**直接构造控制器**（同包测试可以直接填 `svc` 字段），
// 用一个实现 `automation.AutomationSvc` 的替身注入任意错误。
//
// 为什么不能少：`code.FromError` 会把"带码错误"透成它的码，把"裸错误"兜成 50000。
// 少了兜底那半，一个没包 error 的第三方库错误会以 `code:-1` 出去（客户端拿到的是
// "未知错误"而不是"我们这边出事了"），而所有 happy path 用例照样绿。

type stubSvc struct{ err error }

func (s stubSvc) ListAutomations() ([]dtoautomation.AutomationResp, error) { return nil, s.err }
func (s stubSvc) GetAutomation(string) (*dtoautomation.AutomationResp, error) {
	return nil, s.err
}
func (s stubSvc) CreateAutomation(context.Context, dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error) {
	return nil, s.err
}
func (s stubSvc) UpdateAutomation(context.Context, string, dtoautomation.AutomationWriteReq) (*dtoautomation.AutomationResp, error) {
	return nil, s.err
}
func (s stubSvc) DeleteAutomation(context.Context, string) (*dtoautomation.AutomationDeleteResp, error) {
	return nil, s.err
}
func (s stubSvc) PreviewAutomation(dtoautomation.AutomationPreviewReq) (*dtoautomation.AutomationPreviewResp, error) {
	return nil, s.err
}
func (s stubSvc) ListAutomationRuns(string) (*dtoautomation.AutomationRunsResp, error) {
	return nil, s.err
}
func (s stubSvc) RunAutomationNow(context.Context, string) (*dtoautomation.AutomationRunResp, error) {
	return nil, s.err
}

// doAutomation 用**被测控制器本身**挂一条路由（不经 router 包：这一条要换掉用例实现）。
func doAutomation(t *testing.T, svc stubSvc) (code int) {
	t.Helper()
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	ctr := &automationCtr{svc: svc}
	r.GET("/automations", ctr.ListAutomations)
	req := httptest.NewRequest(http.MethodGet, "/automations", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应不是信封 JSON: %s (%v)", w.Body.String(), err)
	}
	return env.Code
}

func TestAutomationErrorExitsCarryDomainCodes(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"载体 CAS 冲突（带码错误原样出）",
			code.NewError(code.ClassLimit, "definition_changed", "定义已被别处改过，请刷新重试", nil), 40901},
		{"裸错误兜底 50000", errors.New("boom"), 50000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := doAutomation(t, stubSvc{err: c.err}); got != c.want {
				t.Fatalf("期望码 %d，got %d", c.want, got)
			}
		})
	}
}
