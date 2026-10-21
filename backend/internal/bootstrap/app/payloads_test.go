package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/config"
	"github.com/morehao/ark-mate/internal/dto/dtosystem"
)

// payloadTestConfig 造一份"能起得来"的最小配置（照 isolation_test 的口径）：
// 内存持久层、临时目录、假资产目录（默认构建没有内嵌资产，缺了会以退出码 3 拒绝启动）。
func payloadTestConfig(t *testing.T) config.Config {
	t.Helper()
	home := t.TempDir()
	web := t.TempDir()
	if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("<!doctype html><title>t</title>"), 0o600); err != nil {
		t.Fatalf("写假资产: %v", err)
	}
	cfg := config.Default()
	cfg.HomeDir = home
	cfg.ConfigDir = filepath.Join(home, "config")
	cfg.TranscriptDir = filepath.Join(home, "transcripts")
	cfg.DBPath = filepath.Join(home, "mate.db")
	cfg.StoreMode = config.StoreModeMemory
	cfg.WorkspaceDir = ""
	cfg.ProcRegistryPath = ""
	cfg.AutomationTick = 0
	cfg.IdleTimeout = 0
	cfg.ReapInterval = 0
	cfg.Addr = "127.0.0.1:0"
	cfg.WebDir = web
	cfg.ProjectionDirs = map[string]string{
		"rtx:skill": filepath.Join(home, "proj-rtx"),
		"dsh:skill": filepath.Join(home, "proj-dsh"),
	}
	return cfg
}

// initPayloadTestApp 走真实装配（App.Init），返回 App；清理与 isolation_test 一致。
func initPayloadTestApp(t *testing.T, opts ...Option) *App {
	t.Helper()
	gin.SetMode(gin.TestMode)
	a := New(payloadTestConfig(t), nil, opts...)
	if err := a.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if a.srv != nil {
			_ = a.srv.Stop(ctx)
		}
		a.Close()
	})
	return a
}

// getMetaPayloadsRaw 经真实路由取 `/v1/mate/meta`，返回 data.payloads 的**原始 JSON**。
//
// 用原始字节而不是解完再查值：本用例要断言的正是"字段顺序/形状"（nil 切片会变 null、
// 字段顺序变了前端契约就漂移），解析成结构体后这些信息就没了。
func getMetaPayloadsRaw(t *testing.T, a *App) string {
	t.Helper()
	eng := a.Handler()
	if eng == nil {
		t.Fatal("Handler() 为空：Init 没有装配起 HTTP 引擎")
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/mate/meta", nil)
	// M3-5 起 `/v1/mate` 组有 Host 白名单：请求的 Host 必须是**实际监听地址**
	// （httptest 默认填 example.com，那在真实装配下就是 421——这正是白名单该做的事）。
	req.Host = a.srv.Addr()
	eng.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/mate/meta HTTP %d：%s", w.Code, w.Body.String())
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Payloads json.RawMessage `json:"payloads"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("信封解析失败：%v（%s）", err, w.Body.String())
	}
	if len(env.Data.Payloads) == 0 {
		t.Fatalf("响应里没有 payloads 字段（契约要求恒出现）：%s", w.Body.String())
	}
	return string(env.Data.Payloads)
}

// TestMetaPayloadsWiredThroughAssembly 载荷清单经真实装配透出到 `/meta`（M3-4）。
//
// 为什么必须走真实装配：`--payloads` 的三段（命令层解析 → app.Option → metaInfo）里任何一段
// 断掉都不会有编译错误——表现只是"桌面上诊断面永远是空清单"，而那正是最难发现的一种坏。
// 这里断言的是**逐字**的 JSON：字段顺序（name/version/digest/source）与内容都固定，
// entry（壳的包内路径）**不得**出现——`/meta` 的契约里没有它。
func TestMetaPayloadsWiredThroughAssembly(t *testing.T) {
	payloads := []dtosystem.PayloadInfo{
		{Name: "dsh", Version: "0.1.7-rc.2", Digest: "sha256:aa", Source: "bundled"},
		{Name: "rtx", Version: "1.2.3", Digest: "sha256:bb", Source: "runtime-env"},
		{Name: "dsh-user", Version: "", Digest: "", Source: "path"},
	}
	a := initPayloadTestApp(t, WithPayloads(payloads))

	want := `[{"name":"dsh","version":"0.1.7-rc.2","digest":"sha256:aa","source":"bundled"},` +
		`{"name":"rtx","version":"1.2.3","digest":"sha256:bb","source":"runtime-env"},` +
		`{"name":"dsh-user","version":"","digest":"","source":"path"}]`
	if got := getMetaPayloadsRaw(t, a); got != want {
		t.Errorf("payloads 未原样透出：\n got %s\nwant %s", got, want)
	}
	// 直接读装配值也必须一致（免得只有 HTTP 路径对）。
	if got := a.Meta().Payloads; len(got) != len(payloads) {
		t.Fatalf("metaInfo().Payloads 条数不符: %+v", got)
	}
}

// TestMetaPayloadsEmptyIsArrayNotNil 非桌面态（不传 --payloads）下 `/meta` 的 payloads 是 `[]`。
//
// 空数组与 null 在 JS 里不是一回事（`data.payloads.length` 对 null 会抛），契约要求恒为数组。
func TestMetaPayloadsEmptyIsArrayNotNil(t *testing.T) {
	a := initPayloadTestApp(t)
	if got := getMetaPayloadsRaw(t, a); got != "[]" {
		t.Errorf("未传载荷时 payloads 应为 []，实际 %s", got)
	}
	// 形状约定的第二道：壳/前端也可能直接读装配值，那里同样不该是 nil。
	if got := a.Meta().Payloads; got == nil {
		t.Error("metaInfo().Payloads 为 nil：直接消费装配值的调用方会看到 null 语义")
	} else if len(got) != 0 {
		t.Errorf("未传载荷时应为空清单，实际 %+v", got)
	}
	// 反向哨兵：确认这不是"整份 /meta 都空着"——版本字段是真值，只有载荷清单为空。
	if got := a.Meta().CoreVersion; got == "" {
		t.Error("/meta 的 coreVersion 为空：说明响应本身没接上，本用例的空数组断言就没有意义")
	}
}
