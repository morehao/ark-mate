package ctrworkspace

import (
	"context"
	"encoding/json"
	"github.com/morehao/ark-mate/internal/core/failure"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/morehao/ark-mate/internal/service/svcworkspace"
)

// pickStub 宿主选择器替身：三个结局里"选定 / 取消"要真的 spawn 宿主命令（osascript/zenity），
// 路由级拿真 picker 跑不出来，所以这三个结局的契约在**控制器级**用替身钉。
type pickStub struct {
	path      string
	cancelled bool
	err       error
}

func (p pickStub) PickDirectoryHost(context.Context) (string, bool, error) {
	return p.path, p.cancelled, p.err
}

func TestWorkspacePickDirectoryThreeOutcomes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	type envelope struct {
		Code int `json:"code"`
		Data struct {
			Path      string `json:"path"`
			Cancelled bool   `json:"cancelled"`
		} `json:"data"`
	}
	for _, tc := range []struct {
		name          string
		stub          pickStub
		wantCode      int
		wantPath      string
		wantCancelled bool
	}{
		{name: "选定", stub: pickStub{path: "/Users/me/proj"}, wantPath: "/Users/me/proj"},
		{name: "取消不是错误", stub: pickStub{cancelled: true}, wantCancelled: true},
		{name: "无可用选择器可判定",
			stub: pickStub{err: failure.NewError(failure.ClassUnsupported, "native_pick_unsupported", "宿主没有可用的原生选择器", nil)},
			// 40501：前端据此回落应用内浏览，而不是笼统 50000。
			wantCode: 40501},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// svc 传 nil：这条路由只经 picker 端口，正是"控制器只做绑定与回响应"的形状。
			r := gin.New()
			r.POST("/pick", NewWorkspaceCtr(svcworkspace.New(nil, tc.stub)).PickDirectory)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/pick", nil))
			var env envelope
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("解析信封: %v body=%s", err, w.Body.String())
			}
			if env.Code != tc.wantCode {
				t.Fatalf("code 不符: got=%d want=%d body=%s", env.Code, tc.wantCode, w.Body.String())
			}
			if env.Data.Path != tc.wantPath || env.Data.Cancelled != tc.wantCancelled {
				t.Fatalf("结局不符: %+v want path=%q cancelled=%v", env.Data, tc.wantPath, tc.wantCancelled)
			}
		})
	}
}
