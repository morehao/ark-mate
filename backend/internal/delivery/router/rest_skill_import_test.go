package router_test

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http/httptest"
	"testing"

	"github.com/morehao/ark-mate/internal/platform/code"
)

// ——— D1 `POST /skills/import` 的传输层契约 ———
//
// 写面本身（解析、同名冲突、critical 零落盘）由 `sessionruntime` 的两种载体夹具覆盖；
// 这里只钉控制器那一半：multipart 读取与"在进入域之前就能拒绝"的两条输入错误。
// `newEngine` 是 memory 模式且没有能力目录，所以成功路径不在这里跑（会在写面报
// `CapabilityDirUnsetErr`）——那是装配事实，不是这条路由的语义。

func multipartPackage(t *testing.T, filename string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("multipart: %v", err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatalf("multipart write: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}
	return &buf, mw.FormDataContentType()
}

func TestImportSkillPackageRouteInputErrors(t *testing.T) {
	e := newEngine(t)

	cases := []struct {
		name     string
		filename string
		data     []byte
	}{
		{name: "缺 file 字段", filename: "", data: nil},
		{name: "后缀不是 zip", filename: "skill.tar.gz", data: []byte("x")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body *bytes.Buffer
			ctype := ""
			if tc.filename == "" {
				body = &bytes.Buffer{}
				mw := multipart.NewWriter(body)
				_ = mw.Close()
				ctype = mw.FormDataContentType()
			} else {
				body, ctype = multipartPackage(t, tc.filename, tc.data)
			}
			req := httptest.NewRequest("POST", "/v1/mate/skills/import", body)
			req.Header.Set("Content-Type", ctype)
			w := httptest.NewRecorder()
			e.ServeHTTP(w, req)

			var env respEnvelope
			if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
				t.Fatalf("响应非 JSON: %v body=%s", err, w.Body.String())
			}
			if env.Code != code.InvalidParam {
				t.Fatalf("应回 40000，got %d（%s）", env.Code, env.Msg)
			}
		})
	}
}
