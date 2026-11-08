package router_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/bootstrap/assembly"
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/core/eventbus"
	"github.com/morehao/ark-mate/internal/drivers/runtimeconn"
	"github.com/morehao/ark-mate/internal/infra/attachmentstore"
	"github.com/morehao/ark-mate/internal/infra/dsh"
	"github.com/morehao/ark-mate/internal/router"
	"github.com/morehao/ark-mate/internal/sessionruntime"
	"github.com/morehao/ark-mate/internal/testutil"
)

// 本文件是 presign 误路由守卫（backend/internal/router/presign_misroute.go）的判据。
//
// 背景：前端预览取字节要打 `POST /v1/mate/files/:id/presign-url`（核心 origin）。若把请求打到了
// 别的宿主，路径会退化成 `/v1/files/<id>/presign-url`（丢 `/mate`）或 `/files/<id>/presign-url`
// ——这些路径在本引擎里没有对应路由，落在 NoRoute。守卫要把它们从误导人的 404 改成明确的
// **405 + 信封**，同时**绝不**误伤真实路由。
//
// 判据分两向（fail-closed + 反证）：
//
//	① 反例：误路由路径 → 405 + 信封 code=40401 + msg 指向"核心 origin"；
//	② 正例：真实路径 `/v1/mate/files/:id/presign-url` 不被守卫拦（落到 ginupload 真 handler）；
//	③ 放行分支：`/v1/mate/` 前缀的非 presign / 非 POST / 非 presign-url 后缀都不触发守卫。

// presignEngine 装配一个**挂了真 ginupload** 的完整引擎（复用 materialApp：真 sqlite 库 + 真附件字节
// 存储 + 真路由表）。它是 ② 正例的前提——只有真引擎里 `/v1/mate/files/:id/presign-url` 才真的被
// ginupload 接住。
func presignEngine(t *testing.T) *gin.Engine {
	t.Helper()
	e, _ := materialApp(t)
	return e
}

// presignCall 发一次请求，回 (HTTP 状态, 信封 code, 原始 body)。body 非 JSON 时 code = -1 但不判死。
func presignCall(t *testing.T, e *gin.Engine, method, path string) (int, int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	e.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	var env struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	code := -1
	body := w.Body.String()
	if err := json.Unmarshal([]byte(body), &env); err == nil {
		code = env.Code
	}
	return w.Code, code, body
}

// TestPresignMisrouteRejected 反例：非核心 origin 形态的 presign 路径一律 405 + 信封，且 msg 指向核心 origin。
//
// 这些就是"前端打错后端"时真实会出现的路径（丢了 `/mate` 段或整个 `/v1/mate` 前缀）。
func TestPresignMisrouteRejected(t *testing.T) {
	e := presignEngine(t)

	cases := []string{
		"/v1/files/file_abc/presign-url", // 丢了 /mate 段（最典型的误路由）
		"/files/file_abc/presign-url",    // 连 /v1 都没了
		"/v1/files/../presign-url",       // 任务标题里那种形态
		"/api/v1/files/file_abc/presign-url",
	}
	for _, p := range cases {
		t.Run(p, func(t *testing.T) {
			status, code, body := presignCall(t, e, http.MethodPost, p)
			if status != http.StatusMethodNotAllowed {
				t.Fatalf("%s 应回 HTTP 405，得到 %d（body=%.200s）", p, status, body)
			}
			if code != 40401 {
				t.Fatalf("%s 的信封 code 应为 40401，得到 %d（body=%.200s）", p, code, body)
			}
			if !strings.Contains(body, "核心 origin") {
				t.Fatalf("%s 的 msg 应提示改用核心 origin，得到 %.200s", p, body)
			}
		})
	}
}

// TestPresignRealRouteNotBlocked 正例：真实 presign 路由不被守卫拦，落到 ginupload 真 handler。
//
// 判据不是"200"（文件不存在时 handler 会回它自己的业务错误），而是"**不是守卫那座 405 山**"：
// 即响应里没有守卫特有的"只挂在核心 origin"文案。这直接证明守卫对真实路由零改动。
func TestPresignRealRouteNotBlocked(t *testing.T) {
	e := presignEngine(t)
	status, _, body := presignCall(t, e, http.MethodPost, "/v1/mate/files/file_abc/presign-url")
	if status == http.StatusMethodNotAllowed && strings.Contains(body, "只挂在核心 origin") {
		t.Fatalf("真实 presign 路由被守卫误拦了（405 + 守卫文案）：%.200s", body)
	}
}

// TestPresignGuardPassesThrough 放行分支：以下都不归守卫管，保持既有去向（非守卫 405）。
//
//   - `/v1/mate/` 前缀的 presign（核心 origin 形态，即便路由不存在也只走原 404，不误报 405）；
//   - 非 POST 的 presign 路径；
//   - POST 但路径不以 `/presign-url` 结尾。
func TestPresignGuardPassesThrough(t *testing.T) {
	e := presignEngine(t)

	// 核心 origin 形态但路由不存在：守卫放行 → 落 writeNotFound（HTTP 404 + 信封 40401），不是守卫 405。
	status, code, body := presignCall(t, e, http.MethodPost, "/v1/mate/files/file_abc/nope/presign-url")
	if status != http.StatusNotFound {
		t.Fatalf("/v1/mate/ 前缀的未知 presign 路径应保持 404，得到 %d（body=%.200s）", status, body)
	}
	if code != 40401 {
		t.Fatalf("应仍是信封 40401，得到 %d（body=%.200s）", code, body)
	}
	if strings.Contains(body, "只挂在核心 origin") {
		t.Fatalf("守卫文案不应出现在核心 origin 形态的请求上：%.200s", body)
	}

	// 非 POST：即便路径像误路由，也不该被守卫改成 405（GET 一个不存在的 presign 路径 → 404）。
	status, _, _ = presignCall(t, e, http.MethodGet, "/v1/files/file_abc/presign-url")
	if status == http.StatusMethodNotAllowed {
		t.Fatalf("GET 误路由路径不应被守卫改成 405，得到 %d", status)
	}

	// POST 但非 presign 后缀：守卫不管。
	status, _, _ = presignCall(t, e, http.MethodPost, "/v1/files/file_abc/serve")
	if status == http.StatusMethodNotAllowed {
		t.Fatalf("非 presign-url 后缀不应被守卫改成 405，得到 %d", status)
	}
}

// TestPresignSignedUrlRoundTrip 端到端回归：上传 → presign → 用签名 URL GET 取回**原始字节**。
//
// 这是 2026-11-08 「文档预览取字节失败（网络错误 / invalid presigned token）」的守卫：
// 之前 attachmentstore.New 只给 local driver 传了 SignSecret，漏了 filestore.WithSignSecret，
// 于是 driver 签发（用它自己的 secret）、filestore 校验（用空 secret）→ GET /objects 恒 403
// "invalid presigned token"，预览即便拿到 URL 也取不到字节。本测试钉死"签发方与校验方 secret 同值"。
func TestPresignSignedUrlRoundTrip(t *testing.T) {
	e := presignEngineWithSign(t)

	// 1) 上传一份字节，拿 file_id
	content := []byte("ark-mate preview round-trip payload")
	sum := sha256.Sum256(content)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("content_hash", hex.EncodeToString(sum[:]))
	fw, _ := mw.CreateFormFile("file", "roundtrip.txt")
	_, _ = fw.Write(content)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/mate/files", &buf)
	req.Header.Set("content-type", mw.FormDataContentType())
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	var env struct {
		Code int `json:"code"`
		Data struct {
			FileID string `json:"file_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil || env.Code != 0 || env.Data.FileID == "" {
		t.Fatalf("上传取 file_id 失败 code=%d body=%s", env.Code, w.Body.String())
	}

	// 2) presign 拿签名 URL
	status, _, body := presignCall(t, e, http.MethodPost, "/v1/mate/files/"+env.Data.FileID+"/presign-url")
	if status != http.StatusOK {
		t.Fatalf("presign 应 200，得到 %d（body=%.300s）", status, body)
	}
	var presignEnv struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &presignEnv); err != nil || presignEnv.Data.URL == "" {
		t.Fatalf("presign 响应里没有 url：%.300s", body)
	}

	// 签名 URL 指向本机 base（127.0.0.1:port）；只取 path+query，交给引擎直接消费，免真实网络。
	u, err := url.Parse(presignEnv.Data.URL)
	if err != nil {
		t.Fatalf("解析签名 url 失败：%v", err)
	}
	target := u.Path
	if u.RawQuery != "" {
		target += "?" + u.RawQuery
	}

	// 3) 用签名 URL GET 取字节：必须是 200 且逐字节等于原始内容（不是 403 invalid presigned token）
	w2 := httptest.NewRecorder()
	e.ServeHTTP(w2, httptest.NewRequest(http.MethodGet, target, nil))
	if w2.Code != http.StatusOK {
		t.Fatalf("签名 URL GET 应 200，得到 %d（body=%.200s）——sign secret 不一致？", w2.Code, w2.Body.String())
	}
	got, _ := io.ReadAll(w2.Body)
	if !bytes.Equal(got, content) {
		t.Fatalf("取回字节与原始内容不符：got=%.80q want=%.80q", got, content)
	}
}

// presignEngineWithSign 与 materialApp 同构，但给 attachmentstore 配上 SignSecret + BaseURL，
// 使 presign 真的能签发可用 URL（默认 materialApp 不配这两项，presign 会 ErrNotSupported）。
func presignEngineWithSign(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dbPath := testutil.TempDBPath(t)
	st := testutil.OpenStore(t, dbPath)
	t.Cleanup(func() { _ = st.Close() })

	if err := attachmentstore.Migrate(st.DB()); err != nil {
		t.Fatalf("filestore.Migrate: %v", err)
	}
	const signSecret = "ark-mate-local-presign"
	attachStore, err := attachmentstore.New(st.DB(), attachmentstore.Options{
		BaseDir:        filepath.Join(t.TempDir(), "attachments"),
		MaxUploadBytes: 20 << 20,
		SignSecret:     signSecret,
		BaseURL:        "http://127.0.0.1:10001/v1/mate/objects",
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
	cfg.HomeDir = t.TempDir()
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
	return router.New(router.Deps{
		Controllers: services.BuildControllers(nil, nil),
		FileStore:   attachStore.FileStore(),
	})
}
