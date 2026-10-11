package router_test

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/morehao/ark-mate/internal/eventbus"
	"github.com/morehao/ark-mate/internal/router"
)

// 开发期资产重建通知的行为判据（P6-A）。
//
// 这些判据**不依赖浏览器也不依赖 dev 循环**：整条链的核心语义（开关默认关闭、判据是内容
// 摘要而不是 mtime、帧里带的是真摘要）都在 HTTP 面上可观察。浏览器那半（EventSource →
// location.reload）由 `ui:accept` 与手工验收钉，见 .dsh/docs/plans/2026-10-11-p6a-dev-asset-notify-plan.md。

// watchEngine 造一个**只挂资产**的引擎（不需要 Service：资产层与领域端口无关）。
func watchEngine(t *testing.T, root string, watch bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	writeDistLayout(t, root)
	assets, err := router.ResolveAssets(root)
	if err != nil {
		t.Fatalf("ResolveAssets: %v", err)
	}
	opts := router.Options{Web: assets}
	if watch {
		opts.AssetWatch = router.NewAssetWatcher(assets, 20*time.Millisecond)
		opts.AssetWatch.Start(t.Context())
	}
	return router.New(router.Ports{}, eventbus.New(), opts)
}

// TestAssetWatchIsOptIn 开关的**默认关闭**语义：没开时这条路径必须仍是"静态资产的 SPA 回落"。
//
// 为什么这条是守卫级判据：这条通道的价值全在开发态，而它的代价（一个永不停歇的轮询循环 +
// 一条端点）在交付态是纯负担；更要紧的是它会让**交付形态多一条不存在于设计里的端点**。
// 判据取 Content-Type 而不是状态码：回落给的是 index.html（200 + text/html），
// 只看 200 会把"通道在"和"回落"混成一个结论。
func TestAssetWatchIsOptIn(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dist")

	off := watchEngine(t, root, false)
	rec := head(t, off, router.AssetWatchPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("未开开关时 %s 应走 SPA 回落（200），得到 %d", router.AssetWatchPath, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("未开开关时 %s 必须是 SPA 回落（text/html），得到 %q —— 通道在交付形态下不该存在",
			router.AssetWatchPath, ct)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("回落也应当是文档的 no-store 口径，得到 %q", rec.Header().Get("Cache-Control"))
	}

	on := watchEngine(t, filepath.Join(t.TempDir(), "dist2"), true)
	rec = head(t, on, router.AssetWatchPath)
	if rec.Code != http.StatusOK {
		t.Fatalf("开着开关时 %s 应回 200，得到 %d", router.AssetWatchPath, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("开着开关时 %s 必须是 text/event-stream，得到 %q", router.AssetWatchPath, ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("SSE 必须是 no-store（这条流的语义就是永不缓存），得到 %q", cc)
	}
	// HEAD 只回头不挂起：这条断言同时钉住"页面侧的探测不会在回环上留一条长连接"。
	if rec.Body.Len() != 0 {
		t.Errorf("HEAD 不该有响应体，得到 %d 字节", rec.Body.Len())
	}
	// 反向：`/dev/assets/events` 不是 API 路径（不该被 v1 组吃掉，也不该与 /v1/mate 契约混）。
	if strings.HasPrefix(router.AssetWatchPath, "/v1/mate") {
		t.Errorf("%s 不该落在 /v1/mate 契约面下（它不进 README 路由表，也不受 REST 凭据约束）",
			router.AssetWatchPath)
	}
}

// TestAssetWatchPublishesOnlyOnContentChange 两段判据的核心语义：
//
//	① **内容没变（只改了 mtime）→ 不推帧**。这正是"判据是内容摘要而不是 mtime"的机器判据：
//	   `vite build --watch` 空转重写产物会让 mtime 变，拿它当判据就会让页面白刷一次。
//	② 内容变了 → 推一帧，且帧里的 digest 与核心 `/meta.webDigest` 的口径**同一个算法**
//	   （CompositeDigest），并且**变了**（不是把启动时那份又推一遍）。
func TestAssetWatchPublishesOnlyOnContentChange(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dist")
	engine := watchEngine(t, root, true)
	srv := httptest.NewServer(engine)
	defer srv.Close()

	resp, err := http.Get(srv.URL + router.AssetWatchPath) //nolint:bodyclose // 由下面的 Close 收
	if err != nil {
		t.Fatalf("连 %s: %v", router.AssetWatchPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q，期望 text/event-stream", ct)
	}

	frames := streamFrames(resp.Body)
	hello := nextFrame(t, frames, 2*time.Second, "首帧 hello")
	if hello.event != "hello" {
		t.Fatalf("首帧事件名 = %q，期望 hello", hello.event)
	}
	base := hello.frame.Digest
	if base == "" {
		t.Fatal("hello 帧没有 digest：启动时就该带上当前那份的摘要（页面自检与人工核对都靠它）")
	}

	js := filepath.Join(root, "assets", "index-abc.js")
	original, err := os.ReadFile(js)
	if err != nil {
		t.Fatalf("读夹具: %v", err)
	}

	// ① 原样重写（内容相同、mtime 变）：等若干拍，**不许**出现 rebuilt。
	if err := os.WriteFile(js, original, 0o644); err != nil {
		t.Fatalf("原样重写: %v", err)
	}
	// 20ms 一拍 → 300ms 足够跑十几拍；若判据错用 mtime，第一拍就会推帧。
	if f, ok := frameWithin(frames, 300*time.Millisecond); ok {
		t.Fatalf("内容没变却推了 %q 帧（digest=%s）——判据必须落在内容摘要上，不是 mtime",
			f.event, f.frame.Digest)
	}

	// ② 真改内容：必须推一帧 rebuilt，且摘要与启动时那份不同。
	changed := append(original, []byte("\nconsole.log('rebuilt')\n")...)
	if err := os.WriteFile(js, changed, 0o644); err != nil {
		t.Fatalf("改内容: %v", err)
	}
	rebuilt := nextFrame(t, frames, 2*time.Second, "内容变化后的 rebuilt")
	if rebuilt.event != "rebuilt" {
		t.Fatalf("事件名 = %q，期望 rebuilt", rebuilt.event)
	}
	if rebuilt.frame.Digest == base {
		t.Fatalf("rebuilt 帧的 digest 与 hello 相同（%s）——说明推的不是重算后的那份", base)
	}
	// 合成摘要的口径与 CompositeDigest 同源：重新解析一遍这份目录应当得到同一个值。
	assets, err := router.ResolveAssets(root)
	if err != nil {
		t.Fatalf("重解析: %v", err)
	}
	if assets.Digest != rebuilt.frame.Digest {
		t.Errorf("帧里的 digest = %s，而重新解析这份目录得到 %s（两者必须同源同算法）",
			rebuilt.frame.Digest, assets.Digest)
	}
	if rebuilt.frame.BuildID == "" {
		t.Error("rebuilt 帧缺少 buildId")
	}
}

// TestAssetWatchStopsWithContext ctx 结束即停：交付/测试里不该留下一个永不停歇的循环。
func TestAssetWatchStopsWithContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "dist")
	writeDistLayout(t, root)
	assets, err := router.ResolveAssets(root)
	if err != nil {
		t.Fatalf("ResolveAssets: %v", err)
	}
	w := router.NewAssetWatcher(assets, 10*time.Millisecond)
	w.Start(t.Context())
	w.Stop()
	w.Stop() // 幂等：重复收尾不该 panic（Close 与 ctx 两条路都可能走到）

	// 收尾之后再取一次摘要：端点仍可服务（它只读内存里的已发布值），不阻塞、不 panic。
	gin.SetMode(gin.TestMode)
	engine := router.New(router.Ports{}, eventbus.New(), router.Options{Web: assets, AssetWatch: w})
	rec := head(t, engine, router.AssetWatchPath)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("收尾后的探测应当照常回 event-stream，得到 %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// --- 夹具 ---

func head(t *testing.T, e *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodHead, path, nil)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, req)
	return w
}

type sseFrame struct {
	event string
	frame struct {
		Digest  string `json:"digest"`
		BuildID string `json:"buildId"`
	}
}

// streamFrames 把响应体解析成帧序列（跳过 `: ping` 注释行）。
//
// 刻意不用 EventSource 的客户端实现：这里要验的是**线上格式**（event 行 + data 行 + 空行），
// 用现成客户端会把"我们写错了格式但客户端恰好容忍"这类问题盖住。
func streamFrames(r io.Reader) <-chan sseFrame {
	out := make(chan sseFrame, 8)
	go func() {
		defer close(out)
		br := bufio.NewReader(r)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if !strings.HasPrefix(line, "event: ") {
				continue // `: ping` 注释或 data 行（data 紧跟 event，见下）
			}
			event := strings.TrimPrefix(line, "event: ")
			dataLine, derr := br.ReadString('\n')
			if derr != nil {
				return
			}
			data := strings.TrimSpace(strings.TrimPrefix(strings.TrimRight(dataLine, "\r\n"), "data: "))
			var f sseFrame
			f.event = event
			if jerr := json.Unmarshal([]byte(data), &f.frame); jerr != nil {
				f.frame.Digest = "!(非法 JSON: " + jerr.Error() + ")"
			}
			out <- f
		}
	}()
	return out
}

func nextFrame(t *testing.T, frames <-chan sseFrame, wait time.Duration, what string) sseFrame {
	t.Helper()
	select {
	case f, ok := <-frames:
		if !ok {
			t.Fatalf("连接在等到 %s 之前就关了", what)
		}
		return f
	case <-time.After(wait):
		t.Fatalf("等 %s 超时（%s）", what, wait)
	}
	return sseFrame{}
}

func frameWithin(frames <-chan sseFrame, wait time.Duration) (sseFrame, bool) {
	select {
	case f, ok := <-frames:
		return f, ok
	case <-time.After(wait):
		return sseFrame{}, false
	}
}
